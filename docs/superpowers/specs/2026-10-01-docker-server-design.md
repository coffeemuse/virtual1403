# Docker support for the virtual1403 server

- **Date:** 2026-10-01
- **Status:** Approved design, pending spec review
- **Branch:** `claude/docker-server` (off `coffeemuse`), merged by PR

## Intent

**Stated by the user**
- Package the server (`webserver/`) so that someone can copy a compose file onto a Linux server, either aarch64 or x86_64, and bring up an instance.
- The immediate user runs it in a private homelab. The setup must also let anyone using the fork run a public instance.
- Images come prebuilt from GHCR and are published by GitHub Actions.
- The container serves plain HTTP only. TLS is planned, but it is out of scope here. Until then, TLS comes from the deployer's existing reverse proxy.
- Approach: a static binary on distroless, with `config.yaml` mounted into the container and no Go code changes.
- Images are tagged `:edge` and `:sha-<short>` only.

**Assumed (not stated)**
- One server container per deployment. Nothing else runs in the compose project.
- The deployer edits a YAML config file. Configuring through environment variables is not required.
- The agent stays a native binary. Only the server is containerized.

**Success means**
- On a fresh Linux host of either architecture, with Docker installed, a deployer can get a running server with these steps:
  1. copy `deploy/docker/`
  2. create `config.yaml` from the sample
  3. run `docker compose up -d`
  4. read the admin credentials from the logs
- That server accepts print jobs from the agent.
- Pushes to `coffeemuse` publish a multi-arch `:edge` image without anyone touching it.

## Scope

**In scope**
- `webserver/Dockerfile`
- root `.dockerignore`
- the `deploy/docker/` bundle: compose file, build override, sample config, README
- the GitHub Actions workflow that tests, builds and publishes the image
- a one-line CLAUDE.md pointer

**Out of scope (possible later work)**
- TLS, whether through the built-in autocert or a bundled proxy
- configuring through environment variables
- an agent image
- versioned or `:latest` image tags, which wait on the fork choosing its own versioning (the fork's current `v0.5.x` tags are upstream's)
- a Docker `HEALTHCHECK`
- automated backups
- any change to server behavior

## Constraints

- **No changes to upstream-owned Go code.** All new files are fork-only. This keeps merges from upstream `master` clean.
- **The server's fixed assumptions stay as they are:**
  - it reads `config.yaml` from its working directory
  - `database_file` is resolved relative to that directory unless the path is absolute
  - templates and static assets are compiled into the binary
  - it logs to stdout
- **No module downloads.** The build uses `vendor/` (`-mod=vendor`).

## Design

### 1. Image: `webserver/Dockerfile`

The build context is the repo root, because the build needs `go.mod`, `vendor/` and `vprinter/`.

**Build stage**
- Base: `FROM --platform=$BUILDPLATFORM golang:1.26`. The major.minor matches the `go` line in `go.mod`.
- Take `ARG TARGETOS TARGETARCH` and compile with:
  ```
  CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -mod=vendor -trimpath -ldflags="-s -w" \
    -o /out/virtual1403-server ./webserver
  ```
  Cross-compiling this way means arm64 builds don't need QEMU.
- Create an empty directory `/out/data` so the runtime stage can copy it.

**Runtime stage**
- Base: `gcr.io/distroless/static-debianNN:nonroot`. Use the newest Debian variant available at implementation time, and nothing older than `debian12`. The image has CA certificates (needed for SMTP STARTTLS) and no shell.
- `COPY` the binary to `/usr/local/bin/virtual1403-server`.
- `COPY --chown=65532:65532` the empty `/out/data` to `/data`. A new named volume mounted there takes this ownership, so the nonroot user can write to it.
- `WORKDIR /etc/virtual1403`. The deployer's `config.yaml` is mounted here.
- `USER nonroot`, `EXPOSE 8000`, `ENTRYPOINT ["/usr/local/bin/virtual1403-server"]`.
- Labels:
  - `org.opencontainers.image.source=https://github.com/coffeemuse/virtual1403`
  - `org.opencontainers.image.licenses=GPL-3.0-or-later`
  - `org.opencontainers.image.title`
  - `org.opencontainers.image.description`

**`.dockerignore` (repo root)** excludes:
- `.git`, `.github`, `.claude`, `dist`, `docs`, `deploy`
- `**/*.db`, `**/config.yaml`
- the built binaries listed in `.gitignore`: `agent/agent`, `agent/virtual1403`, `webserver/webserver`, and so on
- `agent/pdfs`, `pdftest/*.pdf`

### 2. Compose bundle: `deploy/docker/`

A deployer copies this directory to the server as-is.

**`compose.yaml`**
```yaml
services:
  server:
    image: ghcr.io/coffeemuse/virtual1403-server:edge
    restart: unless-stopped
    ports:
      - "${V1403_PORT:-8000}:8000"
    volumes:
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
- There is no `build:` key, so the file works on a host that has no source checkout.
- The config mount uses the long syntax with `create_host_path: false`. With the short syntax, a missing `./config.yaml` makes Docker silently create a directory by that name on the host, and the server then fails with a confusing error. With this setting, Compose refuses to start and says the bind source does not exist.
- Setting `V1403_PORT=127.0.0.1:8000` publishes only on loopback. Use that when the reverse proxy runs on the same host.

**`compose.build.yaml`** is a development override used inside the repo:
```yaml
services:
  server:
    build:
      context: ../..
      dockerfile: webserver/Dockerfile
```
Usage: `docker compose -f compose.yaml -f compose.build.yaml up --build`.

**`config.sample.yaml`** starts from `webserver/config.sample.yaml` with these changes:
- `listen_port: 8000`, with a comment that it must match the container port in `compose.yaml`.
- `database_file: /data/virtual1403.db`. The absolute path puts the database on the volume.
- The TLS keys (`tls_listen_port`, `tls_domain`) are removed, with a comment that TLS comes from the reverse proxy for now.
- `server_base_url` is commented as the public URL users reach through the proxy. Verification emails and PDF share links are built from it.
- `# font_file: /etc/virtual1403/<font>.ttf`. To use a font, the deployer adds a matching bind mount.
- `mail_config` is commented with the SMTP requirements (see the README section below).
- The quotas, cleanup settings and nuisance-job settings are copied from the upstream sample unchanged.

**`README.md`** sections:
1. **Prerequisites:** Docker Engine with the Compose plugin, on amd64 or arm64 Linux.
2. **First run:**
   1. Copy the directory.
   2. `cp config.sample.yaml config.yaml` and edit it.
   3. `docker compose up -d`.
   4. `docker compose logs server | grep "Created new admin"`. That line contains the admin email, password and access key.
3. **Reverse proxy:** what to point the proxy at, setting `server_base_url`, and the `V1403_PORT=127.0.0.1:8000` loopback binding.
4. **Email:**
   - SMTP must offer STARTTLS. Port-465 implicit TLS is not supported. A server that needs no authentication also works.
   - `mail_config.disable: true` stops PDF emails only. Verification emails are still sent.
   - So without working SMTP, only the `create_admin` account, which is created already verified, can print.
5. **Upgrades:** `docker compose pull && docker compose up -d`.
6. **Backups:** `docker compose stop`, then copy the volume out with a `docker run --rm -v <project>_data:/data …` tar one-liner, then `docker compose start`.
7. **Troubleshooting:**
   - If `config.yaml` is missing, Compose refuses to start and reports that the bind source path does not exist.
   - If `config.yaml` is invalid, the container logs `FATAL` and keeps restarting. Check the output of `docker compose logs`.
   - The server writes its session and share secret keys to its log at startup. That is upstream behavior. Treat the logs as sensitive.

**CLAUDE.md:** add one line under Commands pointing to `deploy/docker/` and `webserver/Dockerfile`.

### 3. CI: `.github/workflows/server-image.yml`

**Triggers**
- `push` to `coffeemuse`
- `pull_request` into `coffeemuse`
- `workflow_dispatch`

`push` and `pull_request` are limited by `paths` to these files:
- `webserver/**`, `vprinter/**`
- `go.mod`, `go.sum`, `vendor/**`
- `.dockerignore`
- `.github/workflows/server-image.yml`

**Permissions:** `contents: read`, `packages: write`.

**Job `test`**
1. `actions/checkout`
2. `actions/setup-go` with `go-version-file: go.mod`
3. `go vet ./...`
4. `go test ./...`

**Job `image`** (`needs: test`)
1. `actions/checkout`
2. `docker/setup-buildx-action`
3. `docker/login-action` to `ghcr.io` with `GITHUB_TOKEN`. Skipped on `pull_request`.
4. `docker/metadata-action` with image `ghcr.io/coffeemuse/virtual1403-server` and tags:
   - `type=edge,branch=coffeemuse`
   - `type=sha` (produces `sha-<7 chars>`)
5. `docker/build-push-action` with:
   - `context: .`, `file: webserver/Dockerfile`
   - `platforms: linux/amd64,linux/arm64`
   - `push: ${{ github.event_name != 'pull_request' }}`
   - tags and labels from the metadata step
   - `cache-from: type=gha` and `cache-to: type=gha,mode=max`

Pin each action to its current major version, looked up at implementation time.

On a pull request, the workflow tests and builds both architectures but publishes nothing.

**Manual step after the first publish:** GHCR creates new packages as private. The repo owner must set `virtual1403-server` to public once so servers can pull without logging in.

## Verification

This change adds no Go code, so there are no new unit tests. Verification means building and running the real thing. The local steps need the Docker daemon running.

1. `docker buildx build --platform linux/amd64,linux/arm64 -f webserver/Dockerfile .` succeeds.
2. Bring up the bundle with `compose.build.yaml`, using a throwaway config in the session scratchpad that is never committed:
   - `mail_config.disable: true`
   - dummy SMTP server and port, so config validation passes
   - `create_admin` set
3. **Smoke checks:**
   - The container stays running, and the log contains `Created new admin account`.
   - `docker inspect` shows user `nonroot`.
   - `GET /`, `/docs/setup` and `/static/pdf.png` return 200.
4. **End-to-end print:** an agent config in online mode with `service_address: http://localhost:8000/print` and the admin access key taken from the log. Run:
   ```
   go run ./agent -config <scratch>/agent.yaml -printfile <scratch>/sample.txt
   ```
   The agent should log a 200 response, and the server should log that the job was processed.
5. **Persistence:**
   - `docker compose down` (without `-v`) then `up -d`. The log shows `admin account … already exists`.
   - A throwaway container shows `/data` owned by UID 65532.
6. **Failure modes:**
   - With no `deploy/docker/config.yaml`, `docker compose up` refuses to start. It reports a missing bind source and creates no stray directory.
   - With an invalid config, for example `server_base_url` removed, the container exits with a `FATAL` log line.
7. **amd64 under emulation:** the amd64 image starts and serves `/` on the arm64 development Mac.
8. **CI:**
   - The PR run passes `test` and builds both architectures without pushing.
   - After merge, `:edge` and `:sha-…` exist on GHCR.
   - `docker manifest inspect` lists `linux/amd64` and `linux/arm64`.
9. **Done by the user:** set the package to public, then do the first deployment on a homelab server.

## Follow-ups (not part of this work)

- TLS: either built-in autocert with ports 80 and 443 published, or a Caddy profile.
- A fork versioning scheme, then versioned and `:latest` image tags.
- Optional environment-variable configuration.
- An agent image.
- A `-config` flag for the server.
