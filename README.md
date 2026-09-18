# singerOS

`singerOS` is a Go/Gin web service for Tesla-oriented microphone capture, vocal recording and local karaoke playback.

## Production model

Production nodes do **not** clone or compile the repository. GitHub Actions builds one static Linux/amd64 binary and publishes two distribution packages:

- `singerOS-linux-amd64-centos9.tar.gz`
- `singerOS-linux-amd64-ubuntu.tar.gz`

`SHA256SUMS` is published beside both packages. The rolling `edge` release tracks the latest `main`; tags matching `v*` create immutable releases.

## Compose

The repository contains runtime-only Compose definitions. They use `alpine:3.22` as a minimal container shell around the prebuilt static binary; no source tree or Go toolchain is required on deployment nodes.

- `deploy/compose-centos9.yaml`: internal HTTP `:8080`, attached to external `singeros_net` for the RN reverse proxy.
- `deploy/compose-ubuntu.yaml`: direct HTTPS `:8443` using `/opt/singeros/certs/fullchain.pem` and `privkey.pem`.

The root `compose.yaml` is the generic HTTP runtime definition.

## Release-only install

On a supported Ubuntu/Debian or CentOS/RHEL-family host:

```bash
curl -fsSLO https://github.com/wongyiuming/singerOS_by_codex/releases/download/edge/singerOS-linux-amd64-ubuntu.tar.gz
```

For normal production changes, prefer the repository CD workflow rather than manual installation. `scripts/install-release.sh` is included in each release package for recovery/manual deployment and downloads only release assets.

## CI/CD

Every push to `main`:

1. runs `go test ./...`;
2. builds a static Linux/amd64 binary with the exact commit embedded in `buildCommit`;
3. packages Ubuntu and CentOS 9 release archives plus checksums;
4. updates the rolling `edge` GitHub Release;
5. deploys the CentOS package to the RN self-hosted runner;
6. deploys the Ubuntu package to EVOXT through the dedicated RN→EVOXT deployment key when that channel is enabled;
7. verifies the public health endpoints.

Production deployment is therefore artifact-based rather than source-based.

## Health

```text
GET /singeros/healthz
```

A healthy build returns JSON containing `ok: true`, the application name, and the embedded commit SHA.

## Local build

Development only:

```bash
go test ./...
go build -trimpath -o singerOS .
```

Go 1.23 or newer is recommended. Production nodes should use published artifacts instead.
