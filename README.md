# Eregion

The lightweight Go application server for [MithrilPHP](https://github.com/EreborCodeForge/mithrilphp).

> **Build with Mithril. Run in Eregion.**

Eregion receives HTTP requests, keeps a fixed pool of persistent PHP workers warm, speaks length-prefixed MessagePack over Unix domain sockets, applies bounded backpressure, supervises crashes, and exposes health and Prometheus metrics.

## Install (recommended)

The canonical binary source is **GitHub Releases** — not `go build` from a clone. Forge (`forge server:install`) downloads a pinned tag, verifies SHA-256, and installs into `.mithril/bin/eregion`.

| Asset | Platform |
|-------|----------|
| `eregion-linux-amd64` | Linux x86_64 |
| `eregion-linux-arm64` | Linux aarch64 |
| `eregion-darwin-amd64` | macOS Intel |
| `eregion-darwin-arm64` | macOS Apple Silicon |
| `eregion-windows-amd64.exe` | Windows x86_64 |
| `checksums.txt` | SHA-256 of all assets |

Example (Linux amd64, pin a version):

```bash
VERSION=0.1.0
REPO=EreborCodeForge/eregion
curl -fsSL -o eregion \
  "https://github.com/${REPO}/releases/download/v${VERSION}/eregion-linux-amd64"
curl -fsSL -o checksums.txt \
  "https://github.com/${REPO}/releases/download/v${VERSION}/checksums.txt"
sha256sum -c checksums.txt --ignore-missing
chmod +x eregion
./eregion version
```

Version contract (Forge-parseable):

```text
eregion 0.1.0
protocol eregion/1
```

**Protocol:** `eregion/1` (EREGION/1). Production must pin a release tag; do not rely on `latest` alone.

Full distribution contract: [`eregion-binary-distribution.md`](eregion-binary-distribution.md).

## Requirements (from source)

- Go 1.23+
- Linux or macOS (Unix domain sockets; Windows-native pipes are out of scope for v1)
- PHP CLI for workers (provided by MithrilPHP or local fixtures)

## Quick start (development)

```bash
go build -o bin/eregion ./cmd/eregion

# Craft a default eregion.yaml in the project root (or cwd)
./bin/eregion craft
# ./bin/eregion craft --dir=/path/to/app
# ./bin/eregion craft --force

./bin/eregion check --config=eregion.yaml
./bin/eregion serve --config=eregion.yaml --manifest=/path/to/eregion.json
```

CLI:

```text
eregion craft [--dir=PATH] [--force]
eregion serve [--config=eregion.yaml] [--manifest=PATH] [--host] [--port] [--workers]
eregion check [--config=...]
eregion status [--url=http://127.0.0.1:8080/_eregion/health]
eregion version
eregion --version
```

`craft` writes the forge blueprint (`eregion.yaml`):

- without `--dir`: project root if `composer.json` or `go.mod` is found walking up from cwd; otherwise cwd
- with `--dir`: that directory (created if missing)
- refuses overwrite unless `--force`

See also [`eregion.yaml.example`](eregion.yaml.example).

## Configuration notes

- `workers.handshake_timeout` lives under `workers` (not `protocol`)
- Transport and codec are fixed in v1: UDS + MessagePack
- `queue.capacity` counts waiting requests only
- Unknown YAML fields fail startup

## PHP / MithrilPHP side

Este repositório implementa só o binário Go. O que a lib PHP deve implementar (bridge, worker, Forge, manifest, recycle) está em:

- [mithrilphp-eregion-bridge-spec.md](mithrilphp-eregion-bridge-spec.md)

## Development

```bash
make test
make test-race
make build
make dist   # cross-compile release matrix + checksums.txt into dist/
```

Integration tests spawn PHP fixtures under `tests/fixtures/`.

## Releasing

1. Set `VERSION` to `X.Y.Z` (no leading `v`).
2. Commit and push to `main`.
3. Tag and push: `git tag vX.Y.Z && git push origin vX.Y.Z`
4. GitHub Actions builds the asset matrix, writes `checksums.txt`, and publishes the release.

Assets are **immutable** after publication — never rewrite a tag’s binaries.

## License

See [LICENSE](LICENSE).
