# Docker Support for the Server: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a multi-arch distroless image of the virtual1403 server to GHCR, and ship a compose bundle that a deployer copies to a Linux host to run it.

**Architecture:**
- `webserver/Dockerfile` cross-compiles a static binary on the build host's architecture, then copies it onto `distroless/static:nonroot`.
- The deployer's `config.yaml` is bind-mounted read-only into the working directory. The BoltDB file lives on a named volume at `/data`.
- A GitHub Actions workflow tests the code, builds amd64 and arm64, and pushes `:edge` and `:sha-<short>` from the `coffeemuse` branch.
- No Go code changes.

**Tech Stack:** Docker BuildKit/buildx, Docker Compose, distroless, GitHub Actions (`docker/*` actions), GHCR.

**Spec:** `docs/superpowers/specs/2026-10-01-docker-server-design.md`

**Status:** Implemented. The file contents below match the shipped files, including changes made after review: the SMTP wording, the workflow's job-level `packages: write`, its concurrency group, publishing only from `coffeemuse`, the `:dev` tag in `compose.build.yaml`, and the README's font mount and rootless/`userns-remap` notes. If the plan and a shipped file ever disagree, the shipped file is right.

## Global Constraints

**Code and image**
- Do not modify any `*.go` file, `go.mod`, `go.sum`, or anything under `vendor/`.
- Image name: `ghcr.io/coffeemuse/virtual1403-server`. The only tags are `edge` (from the `coffeemuse` branch) and `sha-<7 chars>`.
- Platforms: `linux/amd64` and `linux/arm64`.
- Build stage:
  - base `golang:1.26` on `$BUILDPLATFORM`
  - `CGO_ENABLED=0`
  - `go build -mod=vendor -trimpath -ldflags="-s -w"`
- Runtime base: `gcr.io/distroless/static-debian13:nonroot`. This was the newest Debian variant on 2026-10-01.
- Runtime layout:
  - user `65532:65532`
  - binary `/usr/local/bin/virtual1403-server`
  - `WORKDIR /etc/virtual1403`
  - data at `/data`
  - container port `8000`
- Plain HTTP only. The bundle contains no TLS settings.
- Action versions:
  - `actions/checkout@v7`
  - `actions/setup-go@v7`
  - `docker/setup-buildx-action@v4`
  - `docker/login-action@v4`
  - `docker/metadata-action@v6`
  - `docker/build-push-action@v7`

**Testing and commits**
- Never commit a `config.yaml`, and never commit anything from `$SCRATCH`.
- Test environment:
  - The Docker daemon must be running.
  - Every shell that runs test commands needs:
    ```bash
    export SCRATCH=<scratch dir outside the repo that Docker can bind-mount>
    export V1403_PORT=127.0.0.1:18000
    ```
  - Compose tests use the project name `-p v1403test`, so they never touch a real `virtual1403` project.
- Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  ```

**Refinements beyond the spec**

These are small additions made while planning. They are listed here for review.
1. `compose.yaml` sets `name: virtual1403`. That makes the volume name a predictable `virtual1403_data`, which the README's backup commands rely on.
2. `deploy/docker/.gitignore` ignores `config.yaml`, so a deployer working from a clone can't commit their secrets.
3. The Dockerfile uses the numeric `USER 65532:65532`, matching the `--chown`.
4. The README adds:
   - a step that makes `config.yaml` readable by UID 65532 with mode 600
   - restore commands
   - the option of keeping data in a host directory
   - reverse-proxy notes on body size and timeouts
   - how to connect the agent
5. The workflow's metadata labels override the auto-generated title, description and license so they match the Dockerfile.
6. The CLAUDE.md line moves to Task 3, so that it can mention the workflow.

## Review Focus

These are failure modes the spec implies but doesn't test. Each has a test in the task that owns it.

1. **Config unreadable.** On Linux, a `config.yaml` that the container user (UID 65532) can't read, for example mode 600 owned by the deploying user. The container should log `open config.yaml: permission denied` plus `FATAL`, and the README's `chown 65532:65532` fix should work. Covered in Task 2, Step 9.
2. **Root-owned data directory.** The deployer swaps the named volume for a host directory owned by root. The server should fail with `open /data/virtual1403.db: permission denied`, and the README's `chown` fix should work. Covered in Task 2, Step 9.
3. **Local secrets in the build context.** A developer has `webserver/config.yaml` or a `*.db` file in the checkout. The build context, and therefore the image, must not include them. Covered in Task 1, Steps 3–5.
4. **Stopping and upgrading.** `docker compose down` should finish promptly, because the server must exit on SIGTERM even as PID 1 rather than waiting for the 10-second SIGKILL. The data must survive the container being recreated. Covered in Task 2, Step 7.
5. **Host port 8000 taken, or loopback-only wanted.** `V1403_PORT` should change the published address. All compose tests run on `127.0.0.1:18000` and check the binding. Covered in Task 2, Step 6.

---

### Task 1: Server image (`webserver/Dockerfile`, `.dockerignore`)

**Files:**
- Create: `webserver/Dockerfile`
- Create: `.dockerignore`

**Interfaces:**
- Consumes: nothing.
- Produces: an image built from the repo root with `docker build -f webserver/Dockerfile .`, with this layout:

  | Item | Value |
  |---|---|
  | Entrypoint | `/usr/local/bin/virtual1403-server` |
  | Working directory | `/etc/virtual1403`; the server reads `config.yaml` from here |
  | Writable data directory | `/data`, owned by `65532:65532` |
  | User | `65532:65532` |
  | Exposed port | `8000/tcp` |
  | Build stage name | `build`, used for the context check |

- [ ] **Step 1: Confirm the Docker daemon is running**

Run: `docker info --format '{{.ServerVersion}}'`
Expected: a version number. If you see `failed to connect to the docker API`, stop and ask the user to start Docker Desktop.

- [ ] **Step 2: Write `webserver/Dockerfile`**

```dockerfile
# Image for the virtual1403 server. Build from the repository root:
#   docker build -f webserver/Dockerfile .

# The build stage runs on the build host's architecture and cross-compiles
# for the target platform, so multi-arch builds don't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY . .

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -mod=vendor -trimpath -ldflags="-s -w" \
    -o /out/virtual1403-server ./webserver \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian13:nonroot

LABEL org.opencontainers.image.title="virtual1403-server" \
      org.opencontainers.image.description="Virtual 1403 print server: receives jobs from the virtual1403 agent and emails PDFs" \
      org.opencontainers.image.source="https://github.com/coffeemuse/virtual1403" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"

COPY --from=build /out/virtual1403-server /usr/local/bin/virtual1403-server

# A new named volume mounted at /data takes this ownership, so the server
# can create its database there.
COPY --from=build --chown=65532:65532 /out/data /data

# The server reads config.yaml from its working directory.
WORKDIR /etc/virtual1403

USER 65532:65532
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/virtual1403-server"]
```

- [ ] **Step 3: Plant decoy secrets and show they currently leak into the build context**

Both files are already gitignored. Create them:

```bash
printf 'secret: do-not-ship\n' > webserver/config.yaml
printf 'not a real db\n' > webserver/leak-check.db
```

Build only the build stage, then list what it contains:

```bash
docker build -f webserver/Dockerfile --target build -t v1403-ctx-check .
docker run --rm v1403-ctx-check sh -c 'ls -a /src; ls /src/webserver'
```

Expected (FAIL, because there's no `.dockerignore` yet): the listing includes `.git` and `docs` in `/src`, and `config.yaml` and `leak-check.db` in `/src/webserver`.

- [ ] **Step 4: Write `.dockerignore` at the repo root**

```
# The server image needs only the Go sources, vendor/ and the embedded
# assets. Keep VCS data, docs, deployment files, local configs, databases
# and build output out of the build context.
.git
.github
.claude
.superpowers
dist
docs
deploy
fonts
**/*.db
**/config.yaml
agent/agent
agent/virtual1403
agent/pdfs
pdftest/pdftest
pdftest/*.pdf
webserver/webserver
webserver/testclient/testclient
webserver/dbfixer/dbfixer
```

- [ ] **Step 5: Re-run the context check and remove the decoys**

```bash
docker build -f webserver/Dockerfile --target build -t v1403-ctx-check .
docker run --rm v1403-ctx-check sh -c 'ls -a /src; ls /src/webserver'
rm webserver/config.yaml webserver/leak-check.db
docker image rm v1403-ctx-check
```

Expected (PASS):
- `/src` contains no `.git`, `docs`, `deploy` or `.claude`. It does still contain `go.mod`, `vendor`, `vprinter`, `webserver`, `scanner` and `agent`.
- `/src/webserver` contains no `config.yaml` and no `leak-check.db`.

- [ ] **Step 6: Build the runtime image for the native architecture and check its configuration**

```bash
docker build -f webserver/Dockerfile -t v1403-server-test .
docker image inspect v1403-server-test --format '{{.Config.User}} {{.Config.WorkingDir}} {{json .Config.Entrypoint}} {{json .Config.ExposedPorts}}'
docker image inspect v1403-server-test --format '{{index .Config.Labels "org.opencontainers.image.licenses"}} {{index .Config.Labels "org.opencontainers.image.source"}}'
docker image ls v1403-server-test --format '{{.Size}}'
```

Expected:
- `65532:65532 /etc/virtual1403 ["/usr/local/bin/virtual1403-server"] {"8000/tcp":{}}`
- `GPL-3.0-or-later https://github.com/coffeemuse/virtual1403`
- An image size under 40MB.

- [ ] **Step 7: Run the image with a minimal config and check that it serves and can write `/data`**

```bash
mkdir -p "$SCRATCH/min"
cat > "$SCRATCH/min/config.yaml" <<'EOF'
listen_port: 8000
server_base_url: http://127.0.0.1:18080
database_file: /data/virtual1403.db
create_admin: admin@example.com
server_admin_email: admin@example.com
pdf_cleanup_days: 7
mail_config:
  disable: true
  from_address: virtual.1403@example.com
  server: smtp.example.com
  port: 587
EOF
docker run -d --name v1403-t1 -p 127.0.0.1:18080:8000 \
  -v "$SCRATCH/min/config.yaml:/etc/virtual1403/config.yaml:ro" v1403-server-test
curl -fsS --retry 10 --retry-connrefused --retry-delay 1 -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/docs/setup
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/static/pdf.png
docker logs v1403-t1 2>&1 | grep -c 'Created new admin account'
docker rm -f v1403-t1
```

Expected:
- `200` three times.
- A count of `1`. The admin can only be created after the database was written to `/data`, so this shows `/data` is writable by UID 65532.
- If the logs show `panic: open /data/virtual1403.db: permission denied`, the `--chown` didn't apply to `/data`. Fix the Dockerfile before going on.

- [ ] **Step 8: Build both architectures**

```bash
docker buildx build --platform linux/amd64,linux/arm64 -f webserver/Dockerfile .
```

Expected: the build succeeds for both platforms.

If it fails with `Multi-platform build is not supported for the docker driver`, use a temporary builder instead. Don't change the user's default builder.

```bash
docker buildx create --name v1403-multi --driver docker-container
docker buildx build --builder v1403-multi --platform linux/amd64,linux/arm64 -f webserver/Dockerfile .
docker buildx rm v1403-multi
```

- [ ] **Step 9: Run the amd64 image under emulation**

```bash
docker buildx build --platform linux/amd64 -f webserver/Dockerfile -t v1403-server-test:amd64 --load .
docker image inspect v1403-server-test:amd64 --format '{{.Architecture}}'
docker run -d --name v1403-t1-amd64 --platform linux/amd64 -p 127.0.0.1:18081:8000 \
  -v "$SCRATCH/min/config.yaml:/etc/virtual1403/config.yaml:ro" v1403-server-test:amd64
curl -fsS --retry 15 --retry-connrefused --retry-delay 1 -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18081/
docker rm -f v1403-t1-amd64
docker image rm v1403-server-test v1403-server-test:amd64
```

Expected: `amd64`, then `200`.

- [ ] **Step 10: Commit**

```bash
git status --short   # must show only webserver/Dockerfile and .dockerignore
git add webserver/Dockerfile .dockerignore
git commit -F - <<'EOF'
Add Dockerfile for the server image

Multi-stage build: cross-compiles a static server binary on the build
host's architecture and copies it onto distroless/static:nonroot.
config.yaml is read from /etc/virtual1403 and the database goes in
/data. The .dockerignore keeps local configs, databases and VCS data
out of the build context.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 2: Compose bundle (`deploy/docker/`)

**Files:**
- Create: `deploy/docker/compose.yaml`
- Create: `deploy/docker/compose.build.yaml`
- Create: `deploy/docker/config.sample.yaml`
- Create: `deploy/docker/.gitignore`
- Create: `deploy/docker/README.md`

**Interfaces:**
- Consumes: the image layout from Task 1 (`/etc/virtual1403/config.yaml`, `/data`, port 8000, UID 65532).
- Produces:
  - compose project `virtual1403`, with service `server` and volume `data`, which becomes `virtual1403_data`
  - the `V1403_PORT` environment variable, default `8000`, which sets the published address
  - with `compose.build.yaml`, Compose builds and tags the image locally as `virtual1403-server:dev`, so a dev build never replaces the published `:edge`

- [ ] **Step 1: Write `deploy/docker/compose.yaml`**

```yaml
# virtual1403 server. Copy this directory to your server, create config.yaml
# from config.sample.yaml, then run: docker compose up -d
# See README.md for details.
name: virtual1403

services:
  server:
    image: ghcr.io/coffeemuse/virtual1403-server:edge
    restart: unless-stopped
    ports:
      # Set V1403_PORT=127.0.0.1:8000 to listen on loopback only, e.g. when a
      # reverse proxy on the same host provides TLS.
      - "${V1403_PORT:-8000}:8000"
    volumes:
      # Long syntax so that a missing config.yaml is an error, instead of
      # Docker silently creating a directory by that name.
      - type: bind
        source: ./config.yaml
        target: /etc/virtual1403/config.yaml
        read_only: true
        bind:
          create_host_path: false
      - data:/data

volumes:
  data:
```

- [ ] **Step 2: Write `deploy/docker/compose.build.yaml` and `deploy/docker/.gitignore`**

`deploy/docker/compose.build.yaml`:

```yaml
# Development override: build the image from this checkout instead of
# pulling it. From deploy/docker/:
#   docker compose -f compose.yaml -f compose.build.yaml up --build
# The local tag keeps a dev build from replacing the published :edge image.
services:
  server:
    image: virtual1403-server:dev
    build:
      context: ../..
      dockerfile: webserver/Dockerfile
```

`deploy/docker/.gitignore`:

```
# Your config.yaml holds SMTP credentials; never commit it.
config.yaml
```

- [ ] **Step 3: Write `deploy/docker/config.sample.yaml`**

```yaml
# virtual1403 server configuration for Docker.
#
# Copy this file to config.yaml, next to compose.yaml, and edit it. See
# README.md for the first-run steps.

# Port the server listens on inside the container. Leave this at 8000: it must
# match the container port in compose.yaml. To change the port on the host,
# set V1403_PORT instead (see README.md).
listen_port: 8000

# This setup serves plain HTTP only. For HTTPS, put a TLS-terminating reverse
# proxy in front of the server (see README.md).

# server_base_url is the URL users use to reach this server, without a
# trailing slash. Behind a reverse proxy, this is the proxy's public URL, e.g.
# https://print.example.com. Verification emails and the agent settings shown
# to users are built from it.
server_base_url: http://localhost:8000

# The database lives on the data volume. Don't change this.
database_file: /data/virtual1403.db

# Initial admin email address. If this account does not exist at server
# startup, it will be created as an admin with a random password that is
# printed in the log.
create_admin: admin@example.com

# The following email address will be provided to authenticated users in page
# footers to contact server admin.
server_admin_email: admin@example.com

# font_file is an optional font for the "default-*" profiles. Mount the file
# into the container next to this config (see README.md), then set its path:
#font_file: /etc/virtual1403/my-font.ttf

# Quota - jobs and page count a user is allowed during the quota period.
# Period is in hours. Values <= 0 disable the job and/or page quota.
quota_jobs: 25
quota_pages: 1000
quota_period: 24 # hours

# How many days to keep job PDFs in the database?
pdf_cleanup_days: 7

# Concurrent print jobs will limit the number of simultaneous threads running
# the print API call. The default, 0, will allow unlimited concurrency. A
# higher value will make incoming API requests to the print API block (wait)
# until a seat is available. Typically the default of 0 is fine, but you may
# need to set this due to external factors such as font license compliance,
# mail service limitations, etc.
concurrent_print_jobs: 0

# To prevent users trying to DoS the server with a huge number of overstrike
# lines (thus working around the page quota while sending the server nearly
# unlimited amounts of data), each individual job may be limited to a number
# of print directives. For a 62-lines-per-page virtual printer, a good limit
# here might be 62 * pages quota / 2, assuming users won't typically try to
# blow their entire pages quota on a single job.
max_lines_per_job: 31000

# "Nuisance jobs" are some jobs that run by default on TK4- which produce
# printouts most people don't want to be spammed with. The following is an
# array of regular expressions to identify job names that should be filtered
nuisance_job_names:
  - ^S.*_MF1$
  - ^S.*_TSO$

# The server can automatically delete inactive or unverified users.
#
# If both inactive_months_cleanup and unverified_months_cleanup are not set or
# <= 0, no auto cleanup will occur. If one is set to > 0, both must be set >
# 0.
#
# inactive_months_cleanup is the number of months after which inactive users
# will be deleted.
inactive_months_cleanup: 6
# unverified_months_cleanup is the number of months after which unverified
# accounts will be deleted.
unverified_months_cleanup: 1

# SMTP server for verification emails and PDF delivery. Plaintext and
# STARTTLS servers are supported; implicit TLS (usually port 465) is not. If
# you set username and password, the server must offer STARTTLS (usually
# port 587), because credentials are never sent unencrypted. If
# authentication isn't required, remove username and password.
mail_config:
  from_address: virtual.1403@example.com
  server: smtp.example.com
  port: 587
  username: virtual.1403
  password: change-me
  # Uncomment to stop emailing PDFs. Jobs are still processed and kept for
  # download, but verification emails for new sign-ups are still sent.
  #disable: true
```

- [ ] **Step 4: Write `deploy/docker/README.md`**

````markdown
# Running the virtual1403 server with Docker

This directory has everything you need to run the virtual1403 server on a
Linux host (x86_64 or aarch64) with Docker Compose. The image is published as
`ghcr.io/coffeemuse/virtual1403-server`.

The server speaks plain HTTP. For HTTPS, put it behind a reverse proxy that
handles TLS; see [Reverse proxy](#reverse-proxy).

## Prerequisites

- Docker Engine with the Compose plugin (`docker compose version` works).
- An SMTP server the server can send mail through; see [Email](#email).

## First run

1. Copy this directory to the server, for example to `/opt/virtual1403`.
2. Create your configuration from the sample:

   ```bash
   cp config.sample.yaml config.yaml
   ```

   Edit `config.yaml`. At a minimum, set `server_base_url`, `create_admin`,
   `server_admin_email` and `mail_config`.
3. The container runs as UID 65532, which must be able to read
   `config.yaml`. The file holds your SMTP password, so make it readable by
   that user only:

   ```bash
   sudo chown 65532:65532 config.yaml
   sudo chmod 600 config.yaml
   ```

   From then on, edit it with `sudo`.

   If Docker runs rootless or with `userns-remap`, UID 65532 in the
   container is a different UID on the host, so those commands don't give
   it access:

   - **Rootless Docker:** run them in a container, which applies the same
     UID mapping as the server:

     ```bash
     docker run --rm -v "$PWD/config.yaml":/c busybox \
       sh -c 'chown 65532:65532 /c && chmod 600 /c'
     ```

   - **`userns-remap`:** the host UID is 65532 plus the start of the
     `dockremap` range in `/etc/subuid` (and `/etc/subgid` for the group).
     For `dockremap:100000:65536`, run
     `sudo chown 165532:165532 config.yaml`.
4. Start the server:

   ```bash
   docker compose up -d
   ```

5. The admin account's password and access key are in the log of the first
   start:

   ```bash
   docker compose logs server | grep "Created new admin"
   ```

   The line reads `Created new admin account: <email> ; <password> ; <access
   key>`. Log in at your `server_base_url` and change the password.

The server is now listening on port 8000 of the host. To publish it on a
different port, or on loopback only, set `V1403_PORT` when you start it, or
put it in a `.env` file next to `compose.yaml`:

```bash
V1403_PORT=127.0.0.1:8000 docker compose up -d
```

## Connecting the agent

After logging in, each user's account page shows their agent settings. The
`service_address` is `server_base_url` followed by `/print`, and the
`access_key` is the user's personal key. The agent's own README covers the
rest of its configuration.

## Reverse proxy

Any TLS-terminating reverse proxy (Caddy, Traefik, nginx, and so on) can
forward requests to the server over plain HTTP:

- If the proxy runs on the same host, publish the server on loopback only with
  `V1403_PORT=127.0.0.1:8000`, and proxy to `http://127.0.0.1:8000`.
- Set `server_base_url` to the public `https://` URL. Verification emails and
  the agent settings shown to users are built from it.
- Each print job arrives as a single compressed upload to `/print`, so allow
  request bodies of a few megabytes. nginx's default limit is 1 MB; raise it
  with `client_max_body_size`.
- The server emails the PDF before it answers the agent's request, so keep
  the proxy's read timeout at 60 seconds or more.

Built-in TLS support is planned for a later version of this setup.

## Email

- Plaintext and STARTTLS SMTP servers are supported; implicit TLS (usually
  port 465) is not.
- If you set `username` and `password`, the server must offer STARTTLS
  (usually port 587), because credentials are never sent unencrypted.
  Otherwise sending fails, and the log shows `unencrypted connection`.
- A relay that needs no authentication, such as a LAN mail server on port 25,
  can be plaintext. In that case, remove `username` and `password`.
- `mail_config.disable: true` stops PDFs from being emailed. Jobs are still
  processed and kept for download. Verification emails for new sign-ups are
  still sent.
- Without working email, new users can't verify their accounts. Only the
  `create_admin` account, which is created already verified, can print.

## Optional font

The `default-*` profiles can use a font you supply. Put the font file next to
`config.yaml` and add a bind mount for it under `volumes:` in
`compose.yaml`. Use the long syntax, as for `config.yaml`, so that a missing
or misspelled font file is an error instead of an empty directory that
Docker creates in its place:

```yaml
      - type: bind
        source: ./my-font.ttf
        target: /etc/virtual1403/my-font.ttf
        read_only: true
        bind:
          create_host_path: false
```

Then set `font_file: /etc/virtual1403/my-font.ttf` in `config.yaml`.

## Upgrades

The `edge` tag follows the fork's `coffeemuse` branch and moves with every
change. To upgrade:

```bash
docker compose pull
docker compose up -d
```

To stay on a fixed build, replace `edge` in `compose.yaml` with one of the
`sha-<commit>` tags listed on the package page.

## Data and backups

The database lives in the Docker volume `virtual1403_data`. To back it up
into the current directory:

```bash
docker compose stop
docker run --rm -v virtual1403_data:/data:ro -v "$PWD":/backup busybox \
  tar czf /backup/virtual1403-data.tgz -C /data .
docker compose start
```

To restore that backup:

```bash
docker compose stop
docker run --rm -v virtual1403_data:/data -v "$PWD":/backup busybox \
  sh -c 'rm -rf /data/* && tar xzf /backup/virtual1403-data.tgz -C /data && chown -R 65532:65532 /data'
docker compose start
```

To keep the data in a host directory instead of a named volume, change
`data:/data` in `compose.yaml` to `./data:/data`. Before starting, give the
directory to the container's user:

```bash
mkdir data
sudo chown 65532:65532 data
```

With rootless Docker or `userns-remap`, set the owner as in step 3 of
[First run](#first-run) instead. With rootless Docker, for example:

```bash
docker run --rm -v "$PWD/data":/data busybox chown 65532:65532 /data
```

## Troubleshooting

- **`bind source path does not exist`:** there is no `config.yaml` next to
  `compose.yaml`. Create it as described in [First run](#first-run). If you
  added a font mount, the error can also mean the font file is missing; see
  [Optional font](#optional-font).
- **The container keeps restarting:** check `docker compose logs server`.
  Configuration problems are logged as `ERROR: configuration: ...` followed
  by `FATAL: configuration errors`.
- **`open config.yaml: permission denied`:** UID 65532 can't read
  `config.yaml`. See step 3 of [First run](#first-run).
- **`open /data/virtual1403.db: permission denied`:** UID 65532 can't write
  to the data directory. This happens with a host directory that hasn't been
  handed over to that user; see [Data and backups](#data-and-backups).
- **Sensitive logs:** at every start, the server logs its session and
  download-link secret keys. On the first start, it also logs the admin
  password. Treat the output of `docker compose logs` as sensitive.
````

- [ ] **Step 5: Build through the compose files, then test that a missing config is refused**

Run from the repo root. The build uses both compose files and tags the result `virtual1403-server:dev`. Retag it as the GHCR name so the copied bundle uses it without pulling; Step 10 removes both tags. Then copy the bundle to scratch, as a deployer would, and try to start it with no `config.yaml`:

```bash
docker compose -f deploy/docker/compose.yaml -f deploy/docker/compose.build.yaml build
docker tag virtual1403-server:dev ghcr.io/coffeemuse/virtual1403-server:edge
docker image inspect ghcr.io/coffeemuse/virtual1403-server:edge --format '{{.Config.User}}'
rm -rf "$SCRATCH/deploy" && cp -R deploy/docker "$SCRATCH/deploy"
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" up -d; echo "exit=$?"
test ! -e "$SCRATCH/deploy/config.yaml" && echo "no stray config.yaml"
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" down -v
```

Expected:
- `65532:65532`.
- `up` fails with an error containing `bind source path does not exist`, and prints a non-zero `exit=`.
- `no stray config.yaml`.

- [ ] **Step 6: Test an invalid config, then the README's first-run flow with a valid one**

Invalid config: the sample without `server_base_url` must fail validation.

```bash
cp "$SCRATCH/deploy/config.sample.yaml" "$SCRATCH/deploy/config.yaml"
perl -ni -e 'print unless /^server_base_url:/' "$SCRATCH/deploy/config.yaml"
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" up -d
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" logs --no-log-prefix server | grep -E 'server_base_url is required|FATAL: configuration errors'
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" down -v
```

Expected: both lines appear: `ERROR: configuration: server_base_url is required` and `FATAL: configuration errors`. If `logs` is empty, rerun it; the container may not have started yet.

Valid config: copy the sample, then turn off PDF email and point the base URL at the test port.

```bash
cp "$SCRATCH/deploy/config.sample.yaml" "$SCRATCH/deploy/config.yaml"
perl -pi -e 's/^  #disable: true$/  disable: true/; s|^server_base_url: .*|server_base_url: http://127.0.0.1:18000|' "$SCRATCH/deploy/config.yaml"
grep -nE '^  disable: true$|^server_base_url: http://127.0.0.1:18000$' "$SCRATCH/deploy/config.yaml"
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" up -d
curl -fsS --retry 10 --retry-connrefused --retry-delay 1 -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18000/
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18000/docs/setup
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18000/static/pdf.png
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" port server 8000
docker inspect --format '{{.Config.User}}' "$(docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" ps -q server)"
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" logs server | grep "Created new admin"
```

Expected:
- `grep` shows both edited lines.
- `200` three times.
- `port` prints `127.0.0.1:18000`. That shows `V1403_PORT` controls the published address and the binding is loopback only (Review Focus 5).
- The user is `65532:65532`.
- The README's `grep` command prints one `Created new admin account: admin@example.com ; … ; …` line.

- [ ] **Step 7: Run an end-to-end print with the agent, then check persistence and prompt shutdown**

Print a job through the server with the agent, in online mode, using the admin's access key:

```bash
KEY=$(docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" logs --no-log-prefix server | sed -n 's/.*Created new admin account: .* ; .* ; //p' | head -1)
test -n "$KEY" && echo "got key"
cat > "$SCRATCH/agent.yaml" <<EOF
hercules_address: "127.0.0.1:1403"
mode: "online"
service_address: "http://127.0.0.1:18000/print"
access_key: "$KEY"
profile: "default-green"
EOF
printf 'HELLO FROM DOCKER\nSECOND LINE\n' > "$SCRATCH/sample.txt"
go run ./agent -config "$SCRATCH/agent.yaml" -printfile "$SCRATCH/sample.txt" 2>&1 | grep 'Print API response status'
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" logs --no-log-prefix server | grep 'sent 1 pages to admin@example.com'
```

Expected:
- `got key`.
- The agent prints `INFO:  [fileReader] Print API response status: 200 OK`.
- The server log has `INFO:  sent 1 pages to admin@example.com`. Email is disabled, so nothing is actually sent, but the job is processed and logged.

Persistence and prompt shutdown (Review Focus 4): recreate the container and check the data survived.

```bash
time docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" down
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" up -d
curl -fsS --retry 10 --retry-connrefused --retry-delay 1 -o /dev/null http://127.0.0.1:18000/
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" logs --no-log-prefix server | grep 'admin account admin@example.com already exists'
docker run --rm -v v1403test_data:/data busybox ls -ln /data
```

Expected:
- `down` takes well under 5 seconds of real time. If it takes about 10 seconds, the server is ignoring SIGTERM as PID 1. In that case, add `init: true` under `services.server` in `deploy/docker/compose.yaml`, copy the bundle to `$SCRATCH/deploy` again, and repeat this step.
- `INFO:  admin account admin@example.com already exists`.
- `virtual1403.db` is owned by `65532 65532`.

- [ ] **Step 8: Test the README's backup and restore commands**

These are the README commands, with `-p v1403test` and the `v1403test_data` volume, run from `$SCRATCH`:

```bash
cd "$SCRATCH"
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" stop
docker run --rm -v v1403test_data:/data:ro -v "$PWD":/backup busybox \
  tar czf /backup/virtual1403-data.tgz -C /data .
tar tzf "$SCRATCH/virtual1403-data.tgz"
docker run --rm -v v1403test_data:/data -v "$PWD":/backup busybox \
  sh -c 'rm -rf /data/* && tar xzf /backup/virtual1403-data.tgz -C /data && chown -R 65532:65532 /data'
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" start
curl -fsS --retry 10 --retry-connrefused --retry-delay 1 -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18000/
docker run --rm -v v1403test_data:/data busybox ls -ln /data
cd -
```

Expected:
- `tar tzf` lists `./virtual1403.db`.
- After the restore, `/` returns `200`.
- `virtual1403.db` is owned by `65532 65532`.

- [ ] **Step 9: Test the README's permission fixes (Review Focus 1 and 2)**

These simulate Linux file ownership with volumes, because Docker Desktop's file sharing on macOS hides ownership problems.

Unreadable config: a root-owned file with mode 600 must fail, and the README's `chown` fix must work.

```bash
docker volume create v1403-cfg-test
docker run --rm -i -v v1403-cfg-test:/cfg busybox sh -c 'cat > /cfg/config.yaml && chown 0:0 /cfg/config.yaml && chmod 600 /cfg/config.yaml' < "$SCRATCH/deploy/config.yaml"
docker run --rm -v v1403-cfg-test:/etc/virtual1403:ro ghcr.io/coffeemuse/virtual1403-server:edge 2>&1 | grep -E 'permission denied|FATAL'
docker run --rm -v v1403-cfg-test:/cfg busybox chown 65532:65532 /cfg/config.yaml
docker run -d --name v1403-cfg-ok -v v1403-cfg-test:/etc/virtual1403:ro ghcr.io/coffeemuse/virtual1403-server:edge
docker logs v1403-cfg-ok 2>&1 | grep -c 'Created new admin account'
docker rm -f v1403-cfg-ok
docker volume rm v1403-cfg-test
```

Expected:
- The first run prints `ERROR: configuration: open config.yaml: permission denied` and `FATAL: configuration errors`, then exits.
- After the `chown`, the count is `1`. If it's `0`, rerun the `docker logs` line; the server may not have started yet.

Root-owned data directory: `:nocopy` stops Docker from copying the image's `/data` ownership into the volume, which mimics a host bind mount.

```bash
docker volume create v1403-rootdata-test
docker run --rm -v v1403-rootdata-test:/data busybox sh -c 'chown 0:0 /data && chmod 755 /data'
docker run --rm -v v1403-rootdata-test:/data:nocopy \
  -v "$SCRATCH/deploy/config.yaml:/etc/virtual1403/config.yaml:ro" \
  ghcr.io/coffeemuse/virtual1403-server:edge 2>&1 | grep 'permission denied'
docker run --rm -v v1403-rootdata-test:/data busybox chown 65532:65532 /data
docker run -d --name v1403-data-ok -v v1403-rootdata-test:/data:nocopy \
  -v "$SCRATCH/deploy/config.yaml:/etc/virtual1403/config.yaml:ro" \
  ghcr.io/coffeemuse/virtual1403-server:edge
docker logs v1403-data-ok 2>&1 | grep -c 'Created new admin account'
docker rm -f v1403-data-ok
docker volume rm v1403-rootdata-test
```

Expected:
- The first run panics with `open /data/virtual1403.db: permission denied`.
- After the `chown`, the count is `1`. If it's `0`, rerun the `docker logs` line.

- [ ] **Step 10: Clean up the test resources**

```bash
docker compose -p v1403test -f "$SCRATCH/deploy/compose.yaml" down -v
docker image rm ghcr.io/coffeemuse/virtual1403-server:edge virtual1403-server:dev
rm -rf "$SCRATCH/deploy" "$SCRATCH/agent.yaml" "$SCRATCH/sample.txt" "$SCRATCH/virtual1403-data.tgz"
docker volume ls --format '{{.Name}}' | grep -E '^v1403' || echo "no test volumes left"
```

Expected: `no test volumes left`.

- [ ] **Step 11: Commit**

If Step 7 added `init: true`, `compose.yaml` already includes it.

```bash
git status --short   # must show only the five new files under deploy/docker/
git add deploy/docker/compose.yaml deploy/docker/compose.build.yaml deploy/docker/config.sample.yaml deploy/docker/.gitignore deploy/docker/README.md
git commit -F - <<'EOF'
Add Docker Compose bundle for running the server

deploy/docker/ is what a deployer copies to a Linux host. It contains a
compose file that pulls the GHCR image, a development override that
builds from the checkout, a Docker version of the sample config, and a
README covering first run, reverse proxies, email, upgrades, backups
and troubleshooting.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 3: CI publishing workflow and CLAUDE.md pointer

**Files:**
- Create: `.github/workflows/server-image.yml`
- Modify: `CLAUDE.md`, in the bullet list under `## Commands`, after the `build-agent-dist.sh` bullet

**Interfaces:**
- Consumes: `webserver/Dockerfile` with the repo root as build context (Task 1). `deploy/docker/` exists (Task 2).
- Produces: on pushes to `coffeemuse`, `ghcr.io/coffeemuse/virtual1403-server:edge` and `:sha-<7>` for `linux/amd64` and `linux/arm64`. On PRs into `coffeemuse`, the same build runs without pushing.

- [ ] **Step 1: Write `.github/workflows/server-image.yml`**

```yaml
name: Server image

on:
  push:
    branches: [coffeemuse]
    paths:
      - "webserver/**"
      - "vprinter/**"
      - "go.mod"
      - "go.sum"
      - "vendor/**"
      - ".dockerignore"
      - ".github/workflows/server-image.yml"
  pull_request:
    branches: [coffeemuse]
    paths:
      - "webserver/**"
      - "vprinter/**"
      - "go.mod"
      - "go.sum"
      - "vendor/**"
      - ".dockerignore"
      - ".github/workflows/server-image.yml"
  workflow_dispatch:

permissions:
  contents: read

# One run at a time per branch, so :edge always ends up on the newest
# commit. Superseded pull request runs are cancelled; coffeemuse runs queue.
concurrency:
  group: server-image-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

env:
  IMAGE: ghcr.io/coffeemuse/virtual1403-server

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test ./...

  image:
    needs: test
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v7
      - uses: docker/setup-buildx-action@v4
      # Only coffeemuse publishes. Pull requests, and manual runs from other
      # branches, build without pushing.
      - name: Log in to GHCR
        if: github.event_name != 'pull_request' && github.ref == 'refs/heads/coffeemuse'
        uses: docker/login-action@v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: meta
        uses: docker/metadata-action@v6
        with:
          images: ${{ env.IMAGE }}
          tags: |
            type=edge,branch=coffeemuse
            type=sha
          labels: |
            org.opencontainers.image.title=virtual1403-server
            org.opencontainers.image.description=Virtual 1403 print server: receives jobs from the virtual1403 agent and emails PDFs
            org.opencontainers.image.licenses=GPL-3.0-or-later
      - uses: docker/build-push-action@v7
        with:
          context: .
          file: webserver/Dockerfile
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' && github.ref == 'refs/heads/coffeemuse' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

- [ ] **Step 2: Lint the workflow**

```bash
docker run --rm -v "$PWD:/repo" --workdir /repo rhysd/actionlint:latest -color
echo "exit=$?"
```

Expected: no findings and `exit=0`. If actionlint flags an action input as unknown, check that action's README for its current major version and fix the input name. Don't downgrade the action.

- [ ] **Step 3: Check that the path filters match what the image is built from**

```bash
grep -n '^COPY' webserver/Dockerfile
go list -deps ./webserver | grep '^github.com/racingmars/virtual1403' | sort
```

Expected:
- The only `COPY` from the build context is `COPY . .`, filtered by `.dockerignore`.
- The module's own packages are `vprinter`, `webserver`, `webserver/assets`, `webserver/db`, `webserver/mailer` and `webserver/model`. All of them are covered by the `webserver/**` and `vprinter/**` filters.
- If `go list` shows any other in-module package, add its directory to both `paths:` lists.

- [ ] **Step 4: Add the CLAUDE.md pointer**

In `CLAUDE.md`, find this bullet under `## Commands`:

```markdown
- `build-agent-dist.sh` must stay POSIX `sh` (no bashisms).
```

Insert this bullet directly after it:

```markdown
- Docker: `webserver/Dockerfile` builds the server image, with the repo root as build context. `deploy/docker/` is the compose bundle that deployers copy to a server; see its README. `.github/workflows/server-image.yml` tests the code and publishes `ghcr.io/coffeemuse/virtual1403-server:edge` (amd64 and arm64) on pushes to `coffeemuse`.
```

- [ ] **Step 5: Commit**

```bash
git status --short   # must show only the workflow and CLAUDE.md
git add .github/workflows/server-image.yml CLAUDE.md
git commit -F - <<'EOF'
Add workflow to test and publish the server image to GHCR

Runs go vet and go test, then builds the server image for amd64 and
arm64. Pushes to coffeemuse publish :edge and :sha-<short>; pull
requests build without pushing. Also points CLAUDE.md at the Docker
files.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
EOF
```

---

### Task 4: Pull request, CI check, and post-merge verification

**Files:** none changed.

**Interfaces:**
- Consumes: the commits from Tasks 1–3 on `claude/docker-server`.
- Produces: a PR into `coffeemuse`. After the user merges it, published `:edge` and `:sha-<7>` images.

- [ ] **Step 1: Run a final local check**

```bash
go build ./... && go vet ./... && go test ./...
git diff --stat coffeemuse...HEAD
git diff --name-only coffeemuse...HEAD | grep -E '\.go$|^go\.(mod|sum)$|^vendor/' || echo "no Go changes"
```

Expected:
- Build, vet and test all pass.
- The diff contains only the spec, this plan, the Dockerfile, `.dockerignore`, the five `deploy/docker/` files, the workflow and `CLAUDE.md`.
- `no Go changes`.

- [ ] **Step 2: Push the branch and open the PR**

```bash
git push -u origin claude/docker-server
gh pr create --repo coffeemuse/virtual1403 --base coffeemuse --head claude/docker-server \
  --title "Add Docker image and compose bundle for the server" \
  --body-file - <<'EOF'
Packages the virtual1403 server for Docker, as described in
`docs/superpowers/specs/2026-10-01-docker-server-design.md`.

- `webserver/Dockerfile`: a multi-arch (amd64/arm64) static build on
  `distroless/static:nonroot`. It reads `config.yaml` from
  `/etc/virtual1403` and stores the database in `/data`.
- `deploy/docker/`: the compose bundle that deployers copy to a Linux host,
  with a sample config and a README (first run, reverse proxy, email,
  upgrades, backups, troubleshooting).
- `.github/workflows/server-image.yml`: runs `go vet` and `go test`, then
  builds both architectures. It publishes
  `ghcr.io/coffeemuse/virtual1403-server:edge` and `:sha-<short>` on
  pushes to `coffeemuse`. PRs build without pushing.

No Go code changes. Plain HTTP only; TLS is a follow-up.

After merge: set the `virtual1403-server` package to public in its GHCR
settings so servers can pull it without logging in.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
```

Expected: `gh` prints the PR URL.

- [ ] **Step 3: Bind the PR and read its CI result**

Call `ccd_pr` `get_status`. If it doesn't report the new PR, call `bind_pr` with the PR URL. Read CI when the app reports it; don't poll or schedule checks.

Expected:
- The `test` and `image` jobs succeed.
- The `image` job builds `linux/amd64` and `linux/arm64` and does not push. The GHCR login step is skipped.
- If a job fails, offer Auto-fix, or fix the problem on this branch and push again.

- [ ] **Step 4: Hand the merge to the user**

Report the PR URL and the CI result. Merging is the user's decision. Don't merge, and don't enable auto-merge.

- [ ] **Step 5: After the user merges, verify the publish**

```bash
gh run list --repo coffeemuse/virtual1403 --workflow server-image.yml --branch coffeemuse --limit 1
```

Expected: the latest run on `coffeemuse` shows `completed` and `success`. The local `gh` token has no `read:packages` scope, so check tags in the run log instead:

```bash
gh run view --repo coffeemuse/virtual1403 "$(gh run list --repo coffeemuse/virtual1403 --workflow server-image.yml --branch coffeemuse --limit 1 --json databaseId --jq '.[0].databaseId')" --log | grep -E 'virtual1403-server:(edge|sha-)' | head
```

Expected: lines naming `ghcr.io/coffeemuse/virtual1403-server:edge` and `ghcr.io/coffeemuse/virtual1403-server:sha-<7 chars>`.

- [ ] **Step 6: Once the user has made the package public, check the published manifest**

Ask the user to set the package to public at `https://github.com/users/coffeemuse/packages/container/virtual1403-server/settings`. Then run:

```bash
docker buildx imagetools inspect ghcr.io/coffeemuse/virtual1403-server:edge | grep -E 'Platform:'
```

Expected: `linux/amd64` and `linux/arm64`. The pull needs no login, which shows the package is public.
