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
