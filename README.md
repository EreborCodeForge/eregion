# Eregion

The lightweight Go application server for [MithrilPHP](https://github.com/EreborCodeForge/mithrilphp).

> **Build with Mithril. Run in Eregion.**

Eregion receives HTTP requests, keeps a fixed pool of persistent PHP workers warm, speaks length-prefixed MessagePack over Unix domain sockets, applies bounded backpressure, supervises crashes, and exposes health and Prometheus metrics.

## Requirements

- Go 1.23+
- Linux or macOS (Unix domain sockets; Windows-native pipes are out of scope for v1)
- PHP CLI for workers (provided by MithrilPHP or local fixtures)

## Quick start

```bash
go build -o bin/eregion ./cmd/eregion
./bin/eregion check --config=eregion.yaml
./bin/eregion serve --config=eregion.yaml --manifest=/path/to/eregion.json
```

CLI:

```text
eregion serve [--config=eregion.yaml] [--manifest=PATH] [--host] [--port] [--workers]
eregion check [--config=...]
eregion status [--url=http://127.0.0.1:8080/_eregion/health]
eregion version
```

Copy [`eregion.yaml.example`](eregion.yaml.example) or use [`eregion.yaml`](eregion.yaml).

## Configuration notes

- `workers.handshake_timeout` lives under `workers` (not `protocol`)
- Transport and codec are fixed in v1: UDS + MessagePack
- `queue.capacity` counts waiting requests only
- Unknown YAML fields fail startup

## Development

```bash
make test
make test-race
make build
```

Integration tests spawn PHP fixtures under `tests/fixtures/`.

## License

See [LICENSE](LICENSE).
