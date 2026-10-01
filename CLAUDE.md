# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Virtual 1403 emulates an IBM 1403 line printer for Hercules (and similar mainframe emulators). It reads printer output from a Hercules `sockdev` printer and renders it as green-bar/blue-bar PDFs. One Go module (`github.com/racingmars/virtual1403`) holds two programs:

- **`agent/`**: the user-facing binary (shipped as `virtual1403`). It connects to Hercules over TCP and, for each job, either writes a PDF locally (`mode: local`) or sends the job to the online service (`mode: online`).
- **`webserver/`**: the online service (1403.bitnet.systems). It handles accounts, the `/print` API used by the agent, PDF email delivery, quotas, and an admin UI.

## Fork and branch model

This repo is a friendly fork of upstream [racingmars/virtual1403](https://github.com/racingmars/virtual1403). Over time it will move in its own direction.

- **`master`** mirrors upstream (`upstream/master`). Never commit fork work there; only fast-forward it from upstream.
- **`coffeemuse`** is the fork's default branch (`origin/HEAD`). It holds changes that won't go upstream. Base feature branches and PRs on `coffeemuse`.
- To bring in upstream work: fast-forward `master` from `upstream/master`, then merge `master` into `coffeemuse`.
- The fork diverges gradually. Keep changes to upstream-owned code focused, and avoid drive-by reformatting or renames, so upstream merges stay easy. Keep upstream's copyright headers and attribution.

## Commands

```bash
go build ./...                        # build everything
go vet ./...
go test ./...                         # tests exist only under webserver/ (webserver, mailer, model)
go test ./webserver -run TestTrim     # run a single test
go run ./agent -config agent/config.yaml
go run ./agent -printfile foo.txt [-asa|-cdc] [-output <name>]   # print a local file instead of connecting to Hercules
./build-agent-dist.sh                 # cross-compile agent release archives into dist/v$(cat VERSION)/ (needs zip and unix2dos)
```

- Dependencies are **vendored** (`vendor/modules.txt`). After you change `go.mod`, run `go mod tidy && go mod vendor` and commit `vendor/` too.
- Both programs read `config.yaml` from the working directory. The agent also accepts `-config`; the webserver path is hardcoded. Copy `config.sample.yaml` in either directory to start. Real `config.yaml` files and `*.db` are gitignored.
- `VERSION` holds the release version. Builds inject it with `-ldflags "-X main.version=$VERSION"`. Bumping it is its own commit ("Update version to X.Y.Z").
- `build-agent-dist.sh` must stay POSIX `sh` (no bashisms).

## Architecture

Data flows: Hercules socket → `scanner` → `PrinterHandler` → `vprinter.Job` → PDF.

**`scanner/`**: turns a raw printer byte stream into calls on the `PrinterHandler` interface (`AddLine(line, linefeed)`, `PageBreak()`, `EndOfJob(jobinfo)`), defined in `common.go`. `doc.go` explains the design and has a state diagram.
- The scanner is a state-function machine: each state function takes one byte and returns the next state function. The states for the Hercules socket are in `states.go`. The UTF-8 file scanner's states are in `filestates.go`.
- A bare CR (no linefeed) means overstrike: the next line prints over the current one.
- Lines are limited to 132 bytes.
- `scanner.go` maps a few Hercules bytes above 0x7F to Unicode characters.
- A job ends in one of two ways: (a) the last line before an FF matches `eojRegexp`, the JES2 separator-page trailer for MVS 3.8J/TK4- (this also captures the job type, number and name for `jobinfo`), or (b) a read timeout fires while a job is in progress.
- `filescanner.go`, `asascanner.go` and `cdcscanner.go` handle the agent's `-printfile` mode, which reads plain UTF-8, ASA carriage control, or CDC carriage control. They do no job separation.

**`vprinter/`**: the `Job` interface and its single implementation, `virtual1403` in `1403.go`, which renders with gofpdf on a 14⅞"×11" page with 66 lines.
- `profiles.go` maps profile names (`{default,retro,modern}-{green,blue,plain}[-noskip]`) to font, size, skip-lines and colors. An unknown profile falls back to `default-green`.
- Fonts are embedded with `go:embed`. Only the `default-*` profiles accept a user-supplied font override.
- If you add a profile, also update:
  - the list in `agent/config.sample.yaml`
  - `webserver/assets/html/profiles.page.tmpl`
  - the sample images in `webserver/assets/static/profiles/`

**`agent/`**: `config.go` merges the top-level config, which becomes the input and output named `"default"`, with the optional `inputs:`/`outputs:` arrays into maps keyed by name. `main.go` starts one goroutine per input. Each goroutine reconnects to Hercules every 10s on disconnect and uses one handler:
- `output.go` (`pdfOutputHandler`) writes `v1403-<jobinfo>-<timestamp>.pdf` locally.
- `online.go` (`onlineOutputHandler`) streams the job to the webserver.

Each handler creates a fresh `vprinter.Job` after every `EndOfJob`.

**Agent ↔ webserver wire protocol**: implemented in `agent/online.go` (client), `webserver/printjob.go` (`printjob` and `processPrintDirectives`), and `webserver/testclient/`. A protocol change must keep all three in sync.
- The client sends `POST /print?profile=<name>` with `Authorization: Bearer <access key>`, `Content-Type: text/x-print-job` and `Content-Encoding: zstd`.
- The body has one directive per line:
  - `L:<text>` prints a line, then a linefeed.
  - `O:<text>` prints a line with no linefeed, so the next line overstrikes it.
  - `P:` is a page break.
  - `J:<jobinfo>` ends the job. `jobinfo` must match `^[a-zA-Z0-9_]{0,25}$`.

**`webserver/`**: a `net/http` ServeMux with all routes in `main.go`. The handlers live on the `application` struct, mostly in `ui.go`.
- Sessions use `golangcollege/sessions`.
- Templates and static files are embedded through `webserver/assets` (`embed.FS`). At startup, each `html/*.page.tmpl` is parsed together with every `html/*.layout.tmpl` (`templates.go`). You must rebuild after editing a template.
- When `tls_listen_port > 0`, the server gets TLS certificates with autocert, and the DB doubles as the autocert cache.
- Background goroutines clean up expired PDFs every hour and delete inactive or unverified users every 24h.
- Print jobs are subject to per-user quotas (`quota.go`), `max_lines_per_job`, an optional concurrency semaphore (`printerSeats`), and a nuisance-job regex filter. Users flagged `Unlimited` skip the quota limits.

**`webserver/db/`**: the `db.DB` interface (`db.go`) with a BoltDB implementation (`boltimpl.go`, one bucket per entity). The session-cookie secret and PDF share-link secret are generated on first run and stored in the DB. uint64 keys are big-endian so they sort in order. `webserver/dbfixer/` is a one-time migration for old little-endian databases.

**Scratch tools**: `pdftest/` (it hardcodes a font path) and `webserver/testclient/` (it hardcodes `localhost:4444` and a key) are manual dev utilities, not tests.

## Conventions

- Every source file begins with the GPLv3 copyright header. Keep it on new files.
- Log with `log.Printf` and a level prefix padded to 7 characters: `"INFO:  "`, `"WARN:  "`, `"ERROR: "`, `"FATAL: "`, `"TRACE: "`. In the agent, add an `[inputName]` tag after the prefix. Detailed trace output goes behind the `-trace` flag.
