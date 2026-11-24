# Eregion

The lightweight Go application server for [MithrilPHP](https://github.com/EreborCodeForge/mithrilphp).

> **Build with Mithril. Run in Eregion.**

Eregion receives HTTP requests, supervises **dynamic workloads** (HTTP and consumers), keeps PHP workers warm, speaks length-prefixed MessagePack over Unix domain sockets for HTTP, applies bounded backpressure, scales worker pools, and exposes health and Prometheus metrics.

HTTP is one workload mode. Consumer workloads run a configured argv command (for example `php vendor/bin/job-worker`) without opening an application HTTP listener on that pool.

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
VERSION=0.4.4
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
eregion 0.4.4
protocol eregion/1
```

**Protocol:** `eregion/1` (EREGION/1). Production must pin a release tag; do not rely on `latest` alone.

**0.4.4** keeps protocol `eregion/1` and the existing scaling strategies. `resource_guard` defaults to `observe`. `enforce` refuses new processes that would pass the admission budget, and `hard_limit_mb` is that budget rather than an operating-system memory limit. The VPS and Forge must install this GitHub Release asset into `.mithril/bin/eregion`. Do not benchmark or deploy a binary produced with `go build` from a clone.

The unpublished tree adds opt-in `cpu_utilization`, `latency`, `throughput`, `hybrid`, `scheduled` and `adaptive`. Omitting those blocks leaves `fixed`, `backlog` and `resources` on the previous path. There is no release tag for that work. Local evidence is [`docs/eco-11/RELATORIO.md`](docs/eco-11/RELATORIO.md). The official benchmark artifact is [`docs/eco-11/results/eco11-performance.json`](docs/eco-11/results/eco11-performance.json).

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

- Legacy `php` + `workers` + `queue` config still works and is adapted to a single `http` workload
- Optional `workload_templates` / `workloads` declare additional (or only) workloads; `command` is an argv array (no shell)
- Consumer mode does not speak EREGION/1; the broker client stays in PHP/Mithril
- Published scaling strategies: `fixed`, `backlog` (needs a backlog provider; otherwise stays at `workers.min`), `resources`. The unreleased strategies are documented in [`docs/scaling-strategies.md`](docs/scaling-strategies.md)
- Per-workload metrics: `eregion_workload_*{workload=...}` including desired/running/busy/draining/failed workers, restarts, and scale events
- `workers.handshake_timeout` lives under `workers` (not `protocol`)
- Transport and codec are fixed in v1: UDS + MessagePack
- `queue.capacity` counts **waiting HTTP** requests only; max admitted = `workers.count + queue.capacity` (not a job queue)
- `queue.capacity: 0` means no queue (execute only if a worker is free; otherwise immediate 503)
- `operations.prefix` + relative endpoint paths (e.g. `/metrics`) resolve to `/_eregion/metrics`; absolute paths that already start with the prefix remain valid
- Planned recycle (`max_requests`, `memory_limit_mb`, worker `meta.recycle`) is not a crash; crash loops use `restart_limit` / `restart_window` (slot Failed after more than `restart_limit` crashes in the window)
- Restart backoff resets after a successful worker boot
- `--workers` recomputes derived `queue.capacity` (`count * 8`) when capacity was not set explicitly in YAML
- Unknown YAML fields fail startup
- At startup Eregion **detects** available CPU/memory by resolving the **current process cgroup** (leaf under `/sys/fs/cgroup`, not only the cgroup root; falls back to `GOMAXPROCS` / `NumCPU` outside limits) and logs a **worker sizing recommendation**. This is advisory only: configured worker bounds remain authoritative. `GOMAXPROCS` does **not** cap PHP worker processes. Oversized pools (`workers_per_cpu > 8`) emit WARN but still start. Metrics include `eregion_runtime_cpu_*`, `eregion_runtime_memory_limit_bytes`, `eregion_workers_per_cpu`, and `eregion_workers_recommended{,_min,_max}` (cached once; `eregion_workers_desired` is the HTTP pool desired count)
- `resource_guard` (default `observe`) plans a shared CPU/memory budget without changing worker counts. `enforce` is opt-in and refuses new processes that would pass the admission budget, including the initial boot. It does not kill busy workers and it does not speak a different `EREGION/1`. `off` leaves scaling on the previous path. Missing cgroup data is reported as unknown; it is not treated as zero usage or as infinite capacity.
- `resource_guard.memory.hard_limit_mb` is an admission budget, not a memory limit imposed by the operating system. It does not write `memory.max`, an rlimit, or a cgroup. The kernel can still let an already admitted process grow past that number. In `enforce`, a known cgroup limit and this budget combine as the smaller of the two; without a known cgroup limit, the budget applies only to the measured RSS of the managed process tree. See [`docs/resource-guard.md`](docs/resource-guard.md).

Example `resource_guard` block:

```yaml
resource_guard:
  mode: observe
  sample_interval: 1s
  max_sample_age: 5s
  memory:
    reserve_mb: 256
    hard_limit_mb: 0
    reserve_percent: 10
    worker_fallback_mb: 256
    warning_ratio: 0.80
    critical_ratio: 0.90
    hard_ratio: 0.95
  cpu:
    warning_utilization_ratio: 0.85
    critical_throttling_ratio: 0.20
  prediction:
    enabled: true
    horizon: 60s
    min_samples: 10
  warnings:
    dedupe_window: 60s
```

`eregion check` validates this block and does not start workers or claim a live capacity number.

Example consumer workload (see also [`docs/workloads-spec.md`](docs/workloads-spec.md)):

```yaml
workload_templates:
  io-consumer:
    mode: consumer
    workers: { min: 1, max: 8 }
    resources: { class: io, memory_mb: 128 }
    scaling:
      strategy: backlog
      scale_up_cooldown: 1s
      scale_down_idle_for: 30s

workloads:
  telemetry:
    template: io-consumer
    command:
      - php
      - vendor/bin/job-worker
      - --kernel=App\TelemetryKernel
    queue:
      transport: mqtt
      name: devices/+/temperature
```

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
