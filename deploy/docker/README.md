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
`compose.yaml`:

```yaml
      - ./my-font.ttf:/etc/virtual1403/my-font.ttf:ro
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

## Troubleshooting

- **`bind source path does not exist`:** there is no `config.yaml` next to
  `compose.yaml`. Create it as described in [First run](#first-run).
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
