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
VERSION=0.3.0
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
eregion 0.2.0
protocol eregion/1
```

**Protocol:** `eregion/1` (EREGION/1). Production must pin a release tag; do not rely on `latest` alone.

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
- `queue.capacity` counts **waiting** requests only; max admitted = `workers.count + queue.capacity`
- `queue.capacity: 0` means no queue (execute only if a worker is free; otherwise immediate 503)
- `operations.prefix` + relative endpoint paths (e.g. `/metrics`) resolve to `/_eregion/metrics`; absolute paths that already start with the prefix remain valid
- Planned recycle (`max_requests`, `memory_limit_mb`, worker `meta.recycle`) is not a crash; crash loops use `restart_limit` / `restart_window` (slot Failed after more than `restart_limit` crashes in the window)
- Restart backoff resets after a successful worker boot
- `--workers` recomputes derived `queue.capacity` (`count * 8`) when capacity was not set explicitly in YAML
- Unknown YAML fields fail startup
- At startup Eregion **detects** available CPU/memory by resolving the **current process cgroup** (leaf under `/sys/fs/cgroup`, not only the cgroup root; falls back to `GOMAXPROCS` / `NumCPU` outside limits) and logs a **worker sizing recommendation**. This is advisory only: `workers.count` remains authoritative and is never auto-resized. `GOMAXPROCS` does **not** cap PHP worker processes. Oversized pools (`workers_per_cpu > 8`) emit WARN but still start. Metrics include `eregion_runtime_cpu_*`, `eregion_runtime_memory_limit_bytes`, `eregion_workers_per_cpu`, and `eregion_workers_recommended{,_min,_max}` (cached once; `eregion_workers_desired` is the configured count)

Saturation smoke: `scripts/stress.sh http://127.0.0.1:8080`

## PHP / MithrilPHP side

Este repositório implementa só o binário Go. O worker PHP (bridge MithrilPHP) deve falar EREGION/1 sobre UDS + MessagePack; o protocolo desta release permanece compatível com workers existentes.

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
