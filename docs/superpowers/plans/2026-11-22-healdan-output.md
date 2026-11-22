# Healdan Output Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `healdan` output mode to the agent that files each print job's PDF into a Healdan matter folder, with a local spool so no job is lost.

**Architecture:** A small Healdan HTTP client (`agent/healdanclient.go`) wraps the four API calls. A new `scanner.PrinterHandler` (`agent/healdan.go`) renders each job to a spool directory, uploads it synchronously at `EndOfJob`, and a background ticker retries whatever is left in the spool. Config gains a nested `healdan:` block on each output.

**Tech Stack:** Go standard library only (`net/http`, `mime/multipart`, `encoding/json`, `net/http/httptest` for tests). No new dependencies, so `vendor/` doesn't change.

**Spec:** `docs/superpowers/specs/2026-11-22-healdan-output-design.md`

## Global Constraints

- Every new `.go` file starts with the GPLv3 header used in `agent/output.go`; keep the upstream copyright line and add no reformatting to upstream-owned files.
- Log with `log.Printf`, level prefix padded to 7 characters (`"INFO:  "`, `"WARN:  "`, `"ERROR: "`), then `[inputName]`.
- Never log the password or the token.
- Healdan API base is `<url>/api/v1`; trim a trailing `/` from `url`.
- All Healdan requests after sign-in send `Authorization: Bearer <token>`.
- HTTP client timeout: 60 seconds.
- Document name: `v1403-<jobinfo>-<timestamp>.pdf` (or `v1403-<timestamp>.pdf` when jobinfo is empty), timestamp `time.Now().UTC().Format("20060102T150405")`. (`output.go` uses `T030405`, a 12-hour clock; don't copy that, and don't change `output.go`.)
- Run `go build ./... && go vet ./... && go test ./...` before each commit.
- Commit messages are plain sentences like the repo's history ("Add …"), ending with the `Co-Authored-By` line the session supplies.

## Review Focus

1. Healdan is unreachable when the agent starts: the input must still start and spool jobs, not exit. (Task 3, `TestStartWhileDown`)
2. Two jobs end within the same second with the same jobinfo: the second must not overwrite the first in the spool. (Task 3, `TestSameSecondNames`)
3. The agent is killed while writing a PDF to the spool: a half-written file must never be uploaded. (Task 3, `TestPartialFileIgnored`)
4. A wrong password: exactly one sign-in attempt per process, not one per job or per tick. (Task 3, `TestSignInFailureStopsUploads`)
5. A stray non-PDF file in the spool directory (`.DS_Store`, a subdirectory): ignored, not uploaded, not an error. (Task 3, `TestSpoolIgnoresOtherFiles`)

---

### Task 1: Config

**Files:**
- Modify: `agent/config.go` (`OutputConfig`, `validateConfig`)
- Create: `agent/config_test.go`

**Interfaces:**
- Produces:

```go
type HealdanConfig struct {
	URL        string            `yaml:"url"`
	Username   string            `yaml:"username"`
	Password   string            `yaml:"password"`
	FolderID   int64             `yaml:"folder_id"`
	SpoolDir   string            `yaml:"spool_directory"`
	Properties map[string]string `yaml:"properties"`
}
// added to OutputConfig:
Healdan HealdanConfig `yaml:"healdan"`
```

- [ ] **Step 1: Write the failing tests** in `agent/config_test.go`:
  - `TestLoadHealdanConfig`: write a temp YAML with `hercules_address`, `mode: healdan` and the `healdan:` block from the spec; assert `outputs["default"].Healdan` has `URL`, `Username`, `Password`, `FolderID == 1234`, `SpoolDir`, and `Properties["docclass"] == "Printouts"`.
  - `TestValidateHealdan`: a `healdan` output with every field set yields no errors; with each of `url`, `username`, `password`, `folder_id` (zero), `spool_directory` blanked in turn yields exactly one error naming that key as `'healdan.<key>'`.
  - `TestValidateUnknownMode`: `mode: bogus` still errors.
- [ ] **Step 2:** Run `go test ./agent` — expect compile failure (`HealdanConfig` undefined).
- [ ] **Step 3:** Add the struct and field; in `validateConfig` accept `healdan` as a mode (message becomes `'mode' must be 'local', 'online' or 'healdan'`) and add the five required-field checks. `url` must also parse with scheme `http` or `https`.
- [ ] **Step 4:** Run `go test ./agent` — expect PASS.
- [ ] **Step 5:** Commit: `Add healdan output settings to the agent's config`.

### Task 2: Healdan client

**Files:**
- Create: `agent/healdanclient.go`
- Create: `agent/healdanclient_test.go`

**Interfaces:**
- Produces:

```go
// healdanError is any non-2xx answer. Code is the problem's `code` member
// ("" when the body isn't a problem document).
type healdanError struct {
	Status int
	Code   string
	Detail string
}
func (e *healdanError) Error() string

type healdanField struct {
	Name string `json:"name"`
	Type string `json:"type"` // "user" marks Author, Typist and the like
}

type healdanClient struct { /* base URL, *http.Client, token */ }

func newHealdanClient(baseURL string) *healdanClient
// POST /auth/tokens {"username","password","name": deviceName}; stores the token.
func (c *healdanClient) signIn(username, password, deviceName string) error
// GET /folders/{id}/form; returns the page's items.
func (c *healdanClient) folderForm(folderID int64) ([]healdanField, error)
// GET /users?q=<username>; the id of the hit whose username equals
// username ignoring case; an error naming the username when none does.
func (c *healdanClient) findUserID(username string) (int64, error)
// POST /documents, multipart: part "document" (Content-Type
// application/json: {"folderId","name","properties"}), part "file"
// (filename=name, Content-Type application/pdf). Returns docNumber on 201.
func (c *healdanClient) fileDocument(folderID int64, name string,
	properties map[string]any, pdf io.Reader) (int64, error)
```

- [ ] **Step 1: Write the failing tests** against an `httptest.Server`:
  - `TestSignIn`: server asserts path `/api/v1/auth/tokens`, JSON body with the three members, no `Authorization` header; answers `201 {"token":"hd_x"}`. A following `folderForm` call carries `Authorization: Bearer hd_x`.
  - `TestSignInFailed`: server answers `401` with `application/problem+json` `{"code":"sign-in-failed","detail":"…","status":401}`; `errors.As` yields `*healdanError` with `Status == 401`, `Code == "sign-in-failed"`.
  - `TestFolderForm`: `{"items":[{"name":"author","type":"user"},{"name":"docclass","type":"string"}]}` decodes to two fields (unknown JSON members ignored).
  - `TestFindUserID`: items `jsmithers` (id 7) and `JSmith` (id 9); `findUserID("jsmith") == 9`. No exact match returns an error containing `jsmith`.
  - `TestFileDocument`: server parses the multipart form and asserts the `document` part's Content-Type is `application/json` and decodes to `folderId 1234`, `name "a.pdf"`, `properties {"docclass":"Printouts","author":{"id":9}}`; the `file` part's filename is `a.pdf` and bytes match. Answers `201 {"docNumber":4321}`; the call returns `4321`.
  - `TestNonProblemError`: a `502` with an HTML body gives `*healdanError{Status: 502, Code: ""}`.
  - `TestBaseURLTrailingSlash`: `newHealdanClient(srv.URL + "/")` still requests `/api/v1/auth/tokens`.
- [ ] **Step 2:** Run `go test ./agent -run 'TestSignIn|TestFolderForm|TestFindUserID|TestFileDocument|TestNonProblemError|TestBaseURL'` — expect compile failure.
- [ ] **Step 3:** Implement. Build the multipart body with `multipart.Writer.CreatePart` so each part's `Content-Type` can be set. Read at most 64 KB of an error body.
- [ ] **Step 4:** Run the tests — expect PASS.
- [ ] **Step 5:** Commit: `Add a client for Healdan's document API`.

### Task 3: Output handler and spool

**Files:**
- Create: `agent/healdan.go`
- Create: `agent/healdan_test.go`

**Interfaces:**
- Consumes: `HealdanConfig` (Task 1); `healdanClient`, `healdanError`, `healdanField` (Task 2); `vprinter.NewProfile(profile, font, 11.4)` and `Job.EndJob(io.Writer) (int, error)`.
- Produces:

```go
// Never returns an error for a Healdan that is down: it logs a WARN and
// spools. Returns an error only for a bad profile/font or a spool
// directory it can't create.
func newHealdanOutputHandler(conf HealdanConfig, profile string,
	fontOverride []byte, inputName string) (*healdanOutputHandler, error)

// scanner.PrinterHandler: AddLine, PageBreak, EndOfJob.
// start launches the retry ticker (every retryInterval); stop ends it.
func (h *healdanOutputHandler) start()
func (h *healdanOutputHandler) stop()
// drainSpool tries every waiting file once; exported to tests and to
// -printfile mode. Returns how many files remain.
func (h *healdanOutputHandler) drainSpool() int

var retryInterval = time.Minute // a var so tests can shorten it
```

**Behaviour to implement (the decisions):**

- **Readiness** (`ensureReady`, called before any upload, guarded by the handler's mutex): if not yet ready, sign in with device name `virtual1403 <inputName>`, fetch the form, and resolve properties: a configured name missing from the form is a permanent config error; a name whose field type is `user` is looked up with `findUserID` and sent as `map[string]any{"id": id}`; anything else is sent as the string. A permanent config error (unknown property, unknown user, form `404`/`409`) and a sign-in `401` both set a `halted` flag with an `ERROR` log saying uploads are stopped until restart; while halted, uploads are skipped and jobs keep spooling.
- **EndOfJob:** always re-create the `vprinter.Job` in a `defer`, as `pdfOutputHandler` does. Write the PDF to `<spool>/<name>.part`, close, then rename to `<spool>/<name>`. If `<name>` exists, insert `-2`, `-3`… before `.pdf`. Then upload that one file.
- **Upload result:**
  - `201`: delete the file; `INFO:  [in] filed <name> in Healdan as doc #<n>`.
  - `401` on a non-sign-in call: clear readiness, `ensureReady` once, retry the upload once.
  - network error, `429`, any `5xx`: leave the file; `WARN` with the reason; the ticker retries.
  - any other `4xx`: leave the file; `ERROR` with status, code and detail; remember the name in an in-memory `failed` set that the ticker skips. (A restart retries it, since the set is empty then.)
- **drainSpool:** list the spool directory, take regular files ending `.pdf` only (so `.part` files, other files and subdirectories are ignored), oldest modification time first, skip names in `failed`, upload each. Stop the pass at the first retryable failure.
- One mutex serialises `EndOfJob`'s upload and `drainSpool`.

- [ ] **Step 1: Write the failing tests** in `agent/healdan_test.go`, using a fake Healdan `httptest.Server` helper (`newFakeHealdan(t)`) with switchable behaviour (`signInStatus`, `fileStatuses []int` consumed in order, counters for sign-ins and filings, a slice of received document names and properties) and `t.TempDir()` as the spool. Drive the handler with `AddLine("HELLO", true)` then `EndOfJob("JOB00001_TEST")`.
  - `TestFilesJob`: one filing received, name matches `^v1403-JOB00001_TEST-\d{8}T\d{6}\.pdf$`, body starts with `%PDF`, properties as configured with `author` sent as `{"id":9}`; spool is empty afterwards.
  - `TestEmptyJobinfoName`: `EndOfJob("")` names the document `^v1403-\d{8}T\d{6}\.pdf$`.
  - `TestRetryAfter503`: `fileStatuses = [503, 201]`; after `EndOfJob` the spool holds one file; `drainSpool()` returns 0 and the spool is empty.
  - `TestResignInOn401`: `fileStatuses = [401, 201]`; one `EndOfJob` ends with the spool empty, 2 sign-ins and 2 filing requests.
  - `TestForbiddenStaysAndIsSkipped`: `fileStatuses = [403]`; file remains; a following `drainSpool()` makes no further filing request and returns 1.
  - `TestSignInFailureStopsUploads`: `signInStatus = 401`; two jobs and two `drainSpool()` calls produce exactly 1 sign-in request, 0 filings, 2 spooled files.
  - `TestUnknownPropertyHalts`: config has `properties: {nosuch: x}`; one job; 0 filings, file spooled, later `drainSpool()` makes no requests.
  - `TestStartWhileDown`: server closed before `newHealdanOutputHandler`; constructor returns no error; a job spools one file; point the handler at a live fake (construct with a URL whose listener is started afterwards, e.g. `httptest.NewUnstartedServer`) and `drainSpool()` files it.
  - `TestSameSecondNames`: two `EndOfJob("A")` calls back to back with the server answering `503`; the spool holds two distinct `.pdf` files.
  - `TestPartialFileIgnored`: pre-create `x.pdf.part` in the spool; `drainSpool()` makes no filing request and the file is still there.
  - `TestSpoolIgnoresOtherFiles`: pre-create `.DS_Store` and a subdirectory `old.pdf/`; `drainSpool()` makes no filing request and returns 0.
  - `TestOldestFirst`: pre-create `b.pdf` (older mtime via `os.Chtimes`) and `a.pdf`; received names are `b.pdf` then `a.pdf`.
  - `TestTickerRetries`: set `retryInterval = 20 * time.Millisecond`, `fileStatuses = [503, 201]`, call `start()`; within 2 seconds the spool is empty; `stop()`.
- [ ] **Step 2:** Run `go test ./agent -run 'TestFilesJob|TestEmptyJobinfo|TestRetry|TestResign|TestForbidden|TestSignInFailure|TestUnknownProperty|TestStartWhileDown|TestSameSecond|TestPartial|TestSpoolIgnores|TestOldestFirst|TestTicker'` — expect compile failure.
- [ ] **Step 3:** Implement `agent/healdan.go` per the behaviour list above.
- [ ] **Step 4:** Run `go test -race ./agent` — expect PASS with no race reports.
- [ ] **Step 5:** Commit: `Add the healdan output handler with a retrying spool`.

### Task 4: Wire into the agent and document it

**Files:**
- Modify: `agent/main.go` (output set-up loop near line 99; `runPrinter` near line 178; `runFilePrinter` near line 240)
- Modify: `agent/config.sample.yaml`
- Modify: `agent/README`
- Modify: `CLAUDE.md` (the `agent/` paragraph and the commands note that tests exist only under `webserver/`)

**Interfaces:**
- Consumes: `newHealdanOutputHandler`, `start`, `drainSpool` (Task 3).

- [ ] **Step 1:** In the output set-up loop, treat `healdan` like `local` for the font: run the same font-override loading, and `verifyOrCreateDir(conf.Healdan.SpoolDir)`. Factor the font loading so the `local` branch's behaviour and messages are unchanged.
- [ ] **Step 2:** In `runPrinter`, turn the `if/else` on mode into three branches. The `healdan` branch logs `INFO:  [in] will file PDFs in Healdan at <url>, folder <id>`, builds the handler, calls `drainSpool()` once (files left from a previous run), then `start()`.
- [ ] **Step 3:** In `runFilePrinter`, add the `healdan` branch without `start()`. After the file is scanned and `EndOfJob` has run, if `drainSpool()` returns more than 0, log `WARN:  <n> PDF(s) left in <spool>; they will be filed the next time the agent runs` and exit with status 1.
- [ ] **Step 4:** Add a commented `healdan` example to `agent/config.sample.yaml`: the mode in the list of modes, and the `healdan:` block from the spec with a one-line comment per key. Say that the account needs Read and Write on the folder, and that the config file holds a password so its file permissions should be restricted.
- [ ] **Step 5:** Add a "Healdan mode" section to `agent/README` in the file's existing plain-text style: what it does, the config keys, the spool and what a file left in it means, and the account's access as the spec's "The Healdan account" section gives it.
- [ ] **Step 6:** Update `CLAUDE.md`: add `healdan.go`/`healdanclient.go` to the `agent/` handler list, and change "tests exist only under `webserver/`" to include `agent/`.
- [ ] **Step 7:** Verify: `go build ./... && go vet ./... && go test ./...` all pass. Then a manual smoke run against the fake: none exists outside tests, so instead run `go run ./agent -config <temp config with mode: healdan and url http://127.0.0.1:9> -printfile README.md` and confirm the output: a `WARN` that Healdan can't be reached, one PDF in the spool directory, the "left in" warning, exit status 1.
- [ ] **Step 8:** Commit: `Wire the healdan output mode into the agent and document it`.

### Task 5: Check against a live Healdan (needs the owner's stack)

Not automatable from this repo. With a Healdan dev stack and a service account set up as the spec's "The Healdan account" section says:

- [ ] Run the agent in `-printfile` mode against it and confirm the document appears in the folder with the configured class, subclass, author and typist.
- [ ] Confirm the spec's three open points (the Store rule for a person's row; that the user lookup and folder form answer for a Read-and-Write-only account; the property names for class and subclass) and correct the spec, the sample config and the README with what was found.
- [ ] Commit any corrections: `Correct the Healdan account notes after a live test`.
