# Eregion Application Server — Product and Implementation Specification

**Status:** Draft for implementation  
**Version:** 1.0  
**Date:** 2026-08-02  
**Ecosystem:** EreborCodeForge / MithrilPHP  
**Primary repositories:**
- `EreborCodeForge/mithrilphp`
- `EreborCodeForge/NaryaRuntimeEngine`
- Future repository: `EreborCodeForge/Eregion`

---

## 1. Executive summary

Eregion is the official lightweight application server for MithrilPHP.

It is a Go binary that receives HTTP requests, maintains a fixed pool of persistent PHP workers, dispatches one request at a time to each worker, supervises process health, applies bounded backpressure, recycles unhealthy or aged workers, and exposes operational endpoints for health, readiness, liveness, and metrics.

Eregion is not a generic PHP runtime and must not become a reduced clone of Narya.

Its primary advantage is deep integration with MithrilPHP:

- the application kernel boots once per PHP worker;
- the compiled container remains warm;
- request-bound services live inside isolated scopes;
- the existing MithrilPHP `Worker` remains responsible for application lifecycle;
- Eregion owns HTTP, process supervision, capacity, IPC, timeouts, backpressure, and worker replacement.

The public developer experience must remain inside the MithrilPHP CLI:

```bash
vendor/bin/forge serve
```

The Forge CLI prepares the runtime manifest, validates the application and environment, locates the Eregion binary, and replaces its own process with Eregion.

---

# 2. Product lore and identity

## 2.1 Lore

Eregion was the realm of the greatest Elven smiths of the Second Age. Its artisans, the Gwaith-i-Mírdain, transformed rare materials, knowledge, and craftsmanship into works of exceptional power.

Within the EreborCodeForge ecosystem:

- **MithrilPHP** is the resilient material used to construct applications.
- **Eregion** is the specialized forge where MithrilPHP applications remain warm and ready.
- **Narya** is the broader, more powerful runtime engine for advanced and generic workloads.
- **Durin’s Forge** is an opinionated framework built over MithrilPHP.

Eregion represents specialization, engineering discipline, continuous operation, and the transformation of a lightweight PHP core into a persistent application server.

## 2.2 Official narrative

> Eregion was the realm of the greatest Elven smiths, where mithril, knowledge, and craftsmanship came together.
>
> Eregion Server follows the same principle.
>
> It is the place where MithrilPHP applications are kept warm, shaped for performance, and prepared to serve continuously.
>
> It does not replace the application. It provides the forge in which the application remains alive.

## 2.3 Taglines

Primary:

> **The application server forged for MithrilPHP.**

Supporting:

> **Build with Mithril. Run in Eregion.**

> **Where MithrilPHP stays warm.**

---

# 3. Product boundaries

## 3.1 Eregion is

- A lightweight HTTP application server written in Go.
- The official persistent-worker server for MithrilPHP.
- A fixed PHP worker pool manager.
- A local process supervisor.
- A Unix Domain Socket IPC server.
- A bounded-capacity request dispatcher.
- An operational runtime with health and Prometheus metrics.
- A binary started primarily through `vendor/bin/forge serve`.

## 3.2 Eregion is not

- A PHP framework.
- A replacement for MithrilPHP.
- A generic Laravel, Symfony, or Slim runtime.
- A reverse proxy comparable to Nginx or Envoy.
- A container orchestrator.
- A distributed autoscaler.
- A Kubernetes controller.
- A multi-host worker cluster.
- A second Narya implementation.
- A WebSocket or SSE server in the MVP.
- A dynamic runtime configuration platform in the MVP.

## 3.3 Eregion versus Narya

### Eregion

- Specific to MithrilPHP.
- Opinionated.
- Fixed worker count in v1.
- One application per server process.
- Minimal configuration.
- Small operational API.
- MessagePack over persistent UDS.
- Started through MithrilPHP Forge CLI.
- Uses MithrilPHP lifecycle contracts directly.

### Narya

- Generic runtime.
- Multi-framework integration.
- Dynamic scaling.
- Runtime configuration changes.
- More advanced backpressure strategies.
- Broader operations and runtime features.
- Independent PHP SDK.
- Intended for more advanced and generic deployments.

### Product rule

A feature belongs to Eregion only when it is necessary for safe and practical MithrilPHP execution and useful to most MithrilPHP applications.

Features involving generic framework support, dynamic autoscaling, multi-app hosting, advanced runtime control, or distributed operation belong to Narya.

---

# 4. User experience

## 4.1 Main command

The official way to start a MithrilPHP application is:

```bash
vendor/bin/forge serve
```

Optional overrides:

```bash
vendor/bin/forge serve --host=0.0.0.0 --port=8080 --workers=4
```

Expected output:

```text
MithrilPHP Application Server

✓ Application kernel detected
✓ Compiled container available
✓ PHP 8.3
✓ ext-msgpack enabled
✓ Eregion 0.1.0
✓ 4 workers ready

Server running at http://0.0.0.0:8080
```

## 4.2 Supporting Forge commands

```bash
vendor/bin/forge server:install
vendor/bin/forge server:check
vendor/bin/forge server:status
vendor/bin/forge server:version
vendor/bin/forge optimize
```

The direct Eregion CLI remains available for operations and debugging:

```bash
eregion serve
eregion check
eregion status
eregion version
```

## 4.3 Startup flow

```text
composer install
    ↓
vendor/bin/forge serve
    ↓
validate application kernel
    ↓
validate PHP and ext-msgpack
    ↓
compile container and routes when needed
    ↓
generate runtime manifest
    ↓
resolve Eregion binary
    ↓
replace Forge process with Eregion
    ↓
Eregion starts PHP worker pool
    ↓
workers bootstrap MithrilPHP once
    ↓
workers complete protocol handshake
    ↓
HTTP server becomes ready
```

## 4.4 Process replacement

In containers and production, Forge must replace its own process with Eregion rather than remaining as a parent launcher.

Desired process tree:

```text
PID 1: Eregion
  ├── PHP worker 1
  ├── PHP worker 2
  ├── PHP worker 3
  └── PHP worker 4
```

This is required for:

- direct `SIGTERM` delivery;
- correct graceful shutdown;
- correct container exit code;
- simpler Kubernetes lifecycle;
- avoidance of orphaned PHP processes.

---

# 5. High-level architecture

```text
                          ┌───────────────────────┐
                          │      HTTP Client      │
                          └───────────┬───────────┘
                                      │ HTTP
                                      ▼
┌────────────────────────────────────────────────────────────────┐
│                         Eregion (Go)                           │
│                                                                │
│  ┌─────────────────┐       ┌───────────────────────────────┐  │
│  │ net/http Server │──────▶│ Admission + Dispatcher        │  │
│  └─────────────────┘       └──────────────┬────────────────┘  │
│                                           │                    │
│                                ┌──────────▼───────────┐        │
│                                │ Fixed Worker Pool    │        │
│                                │ idle/busy/draining   │        │
│                                └──────────┬───────────┘        │
│                                           │                    │
│                        persistent UDS + MessagePack             │
└───────────────────────────────────────────┼────────────────────┘
                                            │
                   ┌────────────────────────┼───────────────────┐
                   │                        │                   │
                   ▼                        ▼                   ▼
          ┌─────────────────┐      ┌─────────────────┐  ┌─────────────────┐
          │ PHP Worker 1    │      │ PHP Worker 2    │  │ PHP Worker N    │
          │ Mithril Kernel  │      │ Mithril Kernel  │  │ Mithril Kernel  │
          │ Warm Container  │      │ Warm Container  │  │ Warm Container  │
          └─────────────────┘      └─────────────────┘  └─────────────────┘
```

---

# 6. Responsibility split

## 6.1 Eregion responsibilities

- Accept HTTP connections.
- Enforce HTTP limits and timeouts.
- Maintain bounded request capacity.
- Acquire and release workers.
- Start and supervise PHP processes.
- Detect dead, blocked, or invalid workers.
- Apply restart backoff and crash-loop protection.
- Recycle workers.
- Maintain one persistent UDS connection per worker.
- Encode and decode MessagePack frames.
- Enforce request deadlines.
- Return controlled `502`, `503`, and `504` responses.
- Expose liveness, readiness, health, and metrics.
- Perform graceful shutdown.
- Keep the desired fixed worker count.

## 6.2 MithrilPHP responsibilities

- Resolve and boot the application kernel.
- Keep the application and container warm.
- Convert protocol requests into Mithril HTTP requests.
- Convert Mithril responses into protocol responses.
- Execute `beginScope()` and `endScope()` for every request.
- Count handled requests.
- Measure PHP memory usage.
- Evaluate cooperative recycling policies.
- Report worker metadata and recycling intent.
- Leave the request loop after the last response when recycling.
- Return meaningful process exit codes.

## 6.3 Application responsibilities

- Implement the MithrilPHP `HttpApplication` contract.
- Avoid request state in global or persistent singletons.
- Register request-bound state as scoped.
- Keep boot idempotent.
- Handle application exceptions inside the kernel.
- Avoid unsafe mutable static state.
- Implement idempotency for operations that may be retried by external clients.

---

# 7. Configuration

## 7.1 Recommended v1 configuration

```yaml
version: "1"

server:
  host: "0.0.0.0"
  port: 8080
  read_header_timeout: 10s
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 60s
  shutdown_timeout: 20s
  max_header_bytes: 1048576
  max_body_bytes: 10485760

php:
  binary: "php"
  worker_script: "bin/eregion-worker"
  working_directory: "."
  environment: {}

workers:
  count: 4
  min_ready: 1
  max_requests: 1000
  startup_timeout: 10s
  request_timeout: 30s
  acquire_timeout: 2s
  shutdown_timeout: 5s
  memory_limit_mb: 256
  restart_limit: 5
  restart_window: 30s

  restart_backoff:
    initial: 100ms
    maximum: 5s
    multiplier: 2
    jitter: 0.2

socket:
  directory: "/tmp/eregion"
  directory_permissions: "0700"
  socket_permissions: "0600"

protocol:
  version: 1
  max_frame_bytes: 16777216
  handshake_timeout: 5s

queue:
  capacity: 64
  retry_after: 1s

logging:
  level: "info"
  format: "text"
  access_log: true

operations:
  prefix: "/_eregion"

metrics:
  enabled: true
  path: "/_eregion/metrics"

health:
  enabled: true
  path: "/_eregion/health"

readiness:
  enabled: true
  path: "/_eregion/ready"

liveness:
  enabled: true
  path: "/_eregion/live"
```

## 7.2 Configuration principles

- Every exposed option must affect implemented behavior.
- Unknown fields must produce validation errors by default.
- Invalid duration, permission, port, limit, or path values must fail startup.
- Protocol transport and codec are fixed in v1:
  - transport: Unix Domain Socket;
  - codec: MessagePack.
- JSON fallback must not occur silently.
- Defaults must be safe for local development.
- Production must be able to operate without interactive downloads.

## 7.3 Defaults

When configuration is absent:

```text
host = 127.0.0.1
port = 8080
workers.count = min(runtime.NumCPU(), 8)
workers.min_ready = 1
workers.max_requests = 1000
workers.request_timeout = 30s
workers.acquire_timeout = 2s
queue.capacity = workers.count * 8
```

---

# 8. HTTP server

Eregion should use Go's standard `net/http` package unless a concrete limitation is demonstrated.

Required server settings:

```go
http.Server{
    Addr:              net.JoinHostPort(host, port),
    Handler:           handler,
    ReadHeaderTimeout: readHeaderTimeout,
    ReadTimeout:       readTimeout,
    WriteTimeout:      writeTimeout,
    IdleTimeout:       idleTimeout,
    MaxHeaderBytes:    maxHeaderBytes,
}
```

`max_body_bytes` must be enforced before reading the complete client body.

Required behavior:

- Reject oversized bodies.
- Reject oversized headers.
- Respect client context cancellation.
- Do not allow unbounded in-flight requests.
- Preserve repeated response headers such as `Set-Cookie`.
- Preserve binary response bodies.
- Do not log sensitive headers or bodies by default.

---

# 9. Worker process model

## 9.1 One request per worker

Each PHP worker processes at most one request at a time.

Concurrency is achieved across multiple workers, not inside one PHP process.

```text
worker-1 = busy
worker-2 = idle
worker-3 = busy
worker-4 = idle
```

This protects applications and dependencies that retain state through globals, static properties, singleton services, extensions, or manually managed connections.

## 9.2 Worker identity

Each logical pool slot has a stable ID and a changing generation.

```go
type Worker struct {
    ID                 string
    Generation         uint64
    PID                int
    State              WorkerState
    SocketPath         string
    RequestsHandled    uint64
    StartedAt          time.Time
    RestartCount       uint64
}
```

Example:

```text
worker-2 generation 7 dies
worker-2 generation 8 replaces it
```

Generation prevents stale events from an old process from changing the state of its replacement.

## 9.3 Worker states

```go
type WorkerState string

const (
    WorkerStarting  WorkerState = "starting"
    WorkerIdle      WorkerState = "idle"
    WorkerBusy      WorkerState = "busy"
    WorkerDraining  WorkerState = "draining"
    WorkerDead      WorkerState = "dead"
    WorkerFailed    WorkerState = "failed"
    WorkerStopped   WorkerState = "stopped"
)
```

Semantics:

- `starting`: process exists but has not completed handshake.
- `idle`: ready to receive one request.
- `busy`: processing one request.
- `draining`: no new requests; waiting to finish or exit.
- `dead`: OS process has exited.
- `failed`: slot exceeded restart policy and currently has no healthy process.
- `stopped`: intentional shutdown.

---

# 10. Unix Domain Socket communication

## 10.1 Transport

Communication between Eregion and PHP workers uses persistent Unix Domain Sockets.

One socket is created for each worker generation:

```text
/tmp/eregion/worker-1-1.sock
/tmp/eregion/worker-2-1.sock
/tmp/eregion/worker-3-1.sock
```

Naming:

```text
worker-{slot}-{generation}.sock
```

Directory permissions:

```text
0700
```

Socket permissions:

```text
0600
```

The socket name must be generated internally and never derived from HTTP input.

## 10.2 Connection ownership

Recommended v1 flow:

1. Eregion prepares the private socket directory.
2. Eregion starts the PHP worker with the socket path.
3. PHP creates and listens on the UDS.
4. Eregion waits for the socket to become available.
5. Eregion connects.
6. Eregion sends `hello`.
7. PHP returns `ready`.
8. The worker enters the idle pool.

The UDS connection remains open for the life of that worker generation.

## 10.3 Why UDS

- No per-worker TCP port management.
- Filesystem-level permissions.
- Reduced local attack surface.
- Efficient local IPC.
- Clear separation from stdout/stderr logs.
- Easier worker-specific connection lifecycle.

---

# 11. MessagePack protocol

## 11.1 Protocol decision

Eregion v1 uses:

```text
Transport: persistent Unix Domain Socket
Framing: 4-byte unsigned big-endian payload length
Codec: MessagePack
Protocol identifier: EREGION
Protocol version: 1
```

MessagePack is mandatory for Eregion mode. No silent JSON fallback.

PHP requirement:

```text
ext-msgpack
```

Go library:

```text
github.com/vmihailenco/msgpack/v5
```

## 11.2 Frame layout

```text
┌────────────────────────┬──────────────────────────────┐
│ uint32 big-endian      │ MessagePack payload          │
│ payload byte length    │ exactly N bytes              │
└────────────────────────┴──────────────────────────────┘
```

Read algorithm:

1. Read exactly 4 bytes.
2. Decode unsigned big-endian length.
3. Reject zero or oversized frames where invalid.
4. Compare with `protocol.max_frame_bytes`.
5. Read exactly N bytes.
6. Decode MessagePack.
7. Validate required envelope fields.
8. Process the message.

Writes must handle partial writes. A single `Write` call must not be assumed to send the entire buffer.

## 11.3 Handshake

### Go to PHP

```text
type: hello
protocol: eregion
protocol_version: 1
runtime_version: 0.1.0
worker_id: worker-2
generation: 8
```

### PHP to Go

```text
type: ready
protocol: eregion
protocol_version: 1
worker_id: worker-2
generation: 8
pid: 18432
php_version: 8.3.4
mithril_version: 0.1.0
```

Validation:

- correct message type;
- correct protocol identifier;
- supported protocol version;
- expected worker ID;
- expected generation;
- valid PID;
- completed within `handshake_timeout`.

A process that fails before `ready` never enters the pool.

## 11.4 Request envelope

```go
type RequestEnvelope struct {
    Type          string              `msgpack:"type"`
    Version       uint8               `msgpack:"version"`
    ID            string              `msgpack:"id"`
    Method        string              `msgpack:"method"`
    URI           string              `msgpack:"uri"`
    Path          string              `msgpack:"path"`
    Query         string              `msgpack:"query"`
    Protocol      string              `msgpack:"protocol"`
    Headers       map[string][]string `msgpack:"headers"`
    Body          []byte              `msgpack:"body"`
    RemoteAddress string              `msgpack:"remote_address"`
    Host          string              `msgpack:"host"`
    Scheme        string              `msgpack:"scheme"`
    TimeoutMs     uint32              `msgpack:"timeout_ms"`
}
```

The HTTP body is transported as binary MessagePack data, without Base64.

## 11.5 Response envelope

```go
type ResponseEnvelope struct {
    Type    string              `msgpack:"type"`
    Version uint8               `msgpack:"version"`
    ID      string              `msgpack:"id"`
    Status  uint16              `msgpack:"status"`
    Headers map[string][]string `msgpack:"headers"`
    Body    []byte              `msgpack:"body"`
    Error   *ProtocolError      `msgpack:"error,omitempty"`
    Meta    ResponseMeta        `msgpack:"meta"`
}

type ResponseMeta struct {
    RequestsHandled uint64 `msgpack:"requests_handled"`
    MemoryUsage     uint64 `msgpack:"memory_usage"`
    MemoryPeak      uint64 `msgpack:"memory_peak"`
    Recycle         bool   `msgpack:"recycle"`
    RecycleReason   string `msgpack:"recycle_reason,omitempty"`
}
```

## 11.6 Correlation

Every response must contain the same request ID sent by Eregion.

A mismatched ID is a protocol failure:

```text
request id req-123
response id req-122
→ reject response
→ return 502
→ discard worker
→ replace worker
```

## 11.7 Application error versus protocol error

An HTTP `500` generated by the MithrilPHP kernel is a valid application response and does not automatically invalidate the worker.

A protocol failure includes:

- malformed MessagePack;
- oversized frame;
- unexpected message type;
- missing required field;
- mismatched request ID;
- invalid status;
- connection reset;
- response after deadline;
- incompatible protocol version.

Protocol failures cause worker discard and replacement.

---

# 12. Dispatcher, queue, and backpressure

## 12.1 Queue semantics

`queue.capacity` means the maximum number of requests waiting for a worker.

Requests already executing are bounded separately by `workers.count`.

## 12.2 Admission flow

```text
request arrives
    ↓
queue slot available?
  no → 503 immediately
  yes
    ↓
wait up to acquire_timeout for idle worker
  timeout → 503
  acquired
    ↓
send to worker with request_timeout
```

## 12.3 Required responses

Queue full:

```http
HTTP/1.1 503 Service Unavailable
Retry-After: 1
Content-Type: application/json
```

```json
{
  "error": "server_capacity_exceeded",
  "message": "No worker capacity is currently available."
}
```

Worker execution timeout:

```http
HTTP/1.1 504 Gateway Timeout
```

Worker crash or protocol failure during request:

```http
HTTP/1.1 502 Bad Gateway
```

## 12.4 No automatic request replay

Eregion must not automatically retry a request when a worker dies during processing.

The application may have already produced side effects before the worker exited. Automatic replay could duplicate:

- payments;
- orders;
- writes;
- messages;
- external calls.

Retries belong to clients or upstream infrastructure with explicit idempotency behavior.

---

# 13. Process supervision

## 13.1 Process start

Use `os/exec`.

Conceptual invocation:

```bash
php bin/eregion-worker \
  --manifest=/app/var/runtime/eregion.json \
  --socket=/tmp/eregion/worker-2-8.sock \
  --worker-id=worker-2 \
  --generation=8 \
  --max-requests=1000 \
  --memory-limit-mb=256
```

Capture:

- PID;
- stdout;
- stderr;
- start time;
- exit code;
- signal;
- restart count;
- generation;
- requests handled.

## 13.2 Death detection

Use two complementary signals.

### OS process monitor

A dedicated goroutine waits on:

```go
err := cmd.Wait()
```

This is the authoritative confirmation that the child process exited.

### IPC failure

The active request path may detect death earlier through:

- EOF;
- broken pipe;
- connection reset;
- invalid frame;
- timeout.

When IPC fails, the request fails immediately and the worker is invalidated. The process monitor later confirms exit or the supervisor terminates the process.

## 13.3 Worker death while idle

```text
idle worker exits
    ↓
cmd.Wait returns
    ↓
mark generation dead
    ↓
remove stale availability reference
    ↓
clean socket
    ↓
start replacement with incremented generation
```

`Acquire` must verify that a worker reference is still current, idle, and healthy before returning it.

## 13.4 Worker death while busy

```text
worker exits during request
    ↓
socket returns EOF/reset
    ↓
request receives 502
    ↓
worker marked dead
    ↓
no automatic replay
    ↓
replacement starts
```

## 13.5 Worker alive but unusable

A process may still have a PID but be blocked, looping, or deadlocked.

On request timeout:

1. Return `504`.
2. Mark worker unhealthy.
3. Close IPC connection.
4. Mark worker draining.
5. Send `SIGTERM`.
6. Wait `workers.shutdown_timeout`.
7. Send `SIGKILL` when necessary.
8. Start replacement.

Timed-out workers must not return to the idle pool.

## 13.6 Centralized worker events

Recommended architecture:

```go
type WorkerEvent struct {
    WorkerID   string
    Generation uint64
    Type       WorkerEventType
    Err        error
}

const (
    EventStarted WorkerEventType = iota
    EventReady
    EventExited
    EventTimeout
    EventProtocolFailure
    EventRecycleRequested
)
```

A manager event loop is the authority for state transitions, replacement, metrics, and restart policy.

This avoids multiple goroutines independently mutating pool state.

---

# 14. Restart policy and crash-loop protection

## 14.1 Backoff

Example sequence:

```text
100ms
250ms
500ms
1s
2s
5s
```

Apply configured multiplier and jitter.

## 14.2 Restart window

```yaml
workers:
  restart_limit: 5
  restart_window: 30s
```

When a logical slot exceeds the restart limit inside the window:

- mark slot `failed`;
- stop immediate restart loops;
- expose degraded health;
- keep Eregion running;
- allow controlled later recovery according to implementation policy.

## 14.3 Kubernetes behavior

A single PHP worker crash must not terminate Eregion.

Incorrect:

```text
worker fails
→ Eregion exits
→ pod restarts
→ worker fails
→ CrashLoopBackOff
```

Correct:

```text
worker fails
→ Eregion remains alive
→ slot becomes unavailable
→ restart backoff applies
→ replacement is attempted
→ readiness reflects capacity
```

Eregion should exit only for structural startup failures or unrecoverable server invariants, such as:

- invalid configuration;
- HTTP bind failure;
- PHP binary missing;
- private socket directory unavailable;
- all initial worker slots unable to start after startup policy;
- unrecoverable internal corruption.

---

# 15. Worker recycling

## 15.1 Why recycling remains necessary

Request scope cleanup does not guarantee cleanup of:

- static properties;
- global variables;
- mutable singletons;
- native extension state;
- memory fragmentation;
- caches outside the DI container;
- manually created connections;
- resources not registered with the container.

Therefore Eregion must periodically replace complete PHP processes.

## 15.2 Recycling triggers

A worker may be recycled when:

- `max_requests` is reached;
- PHP-reported memory usage reaches `memory_limit_mb`;
- PHP reports `meta.recycle = true`;
- a request times out;
- protocol state becomes invalid;
- scope cleanup fails;
- worker process crashes;
- Eregion is shutting down;
- future explicit drain or reload is requested.

## 15.3 Planned recycle flow

```text
worker completes current request
    ↓
response contains recycle=true
    ↓
Eregion marks worker draining
    ↓
worker is not returned to idle queue
    ↓
PHP exits its request loop
    ↓
Eregion observes planned exit
    ↓
socket is cleaned
    ↓
new generation starts
```

The response must be sent before PHP exits, ensuring the final successful request is not lost.

## 15.4 Planned recycle versus crash

A planned recycle:

- must not count as a crash;
- must not increment crash-loop failure metrics;
- should not use failure backoff;
- should increment recycle metrics by reason.

A crash:

- increments failure/restart metrics;
- participates in restart-window policy;
- may use exponential backoff.

---

# 16. Graceful shutdown

On `SIGINT` or `SIGTERM`, Eregion must:

1. Mark server draining.
2. Stop accepting new requests.
3. Fail readiness.
4. Allow current requests to finish.
5. Wait up to `server.shutdown_timeout`.
6. Send graceful shutdown to idle workers.
7. Send `SIGTERM` to remaining workers.
8. Wait each worker's shutdown timeout.
9. Send `SIGKILL` where necessary.
10. Remove UDS files.
11. Exit with the correct status.

No worker may be returned to the idle pool after shutdown begins.

---

# 17. Operational endpoints

## 17.1 Liveness

```http
GET /_eregion/live
```

Purpose: indicate whether the Go server process is functioning.

Liveness should generally remain `200` even when PHP capacity is temporarily degraded. This avoids unnecessary pod restarts.

## 17.2 Readiness

```http
GET /_eregion/ready
```

Ready when:

```text
healthy running workers >= workers.min_ready
```

Return `503` when there is insufficient capacity.

## 17.3 Health

```http
GET /_eregion/health
```

Example:

```json
{
  "status": "degraded",
  "workers": {
    "desired": 4,
    "running": 3,
    "idle": 2,
    "busy": 1,
    "starting": 1,
    "failed": 0
  },
  "queue": {
    "waiting": 4,
    "capacity": 64
  }
}
```

Recommended statuses:

- `healthy`;
- `degraded`;
- `unhealthy`.

## 17.4 Metrics

```http
GET /_eregion/metrics
```

Prometheus format.

Minimum metrics:

```text
eregion_http_requests_total
eregion_http_request_duration_seconds
eregion_http_requests_in_flight
eregion_http_errors_total

eregion_workers_desired
eregion_workers_running
eregion_workers_idle
eregion_workers_busy
eregion_workers_starting
eregion_workers_failed

eregion_worker_requests_total
eregion_worker_restarts_total
eregion_worker_recycles_total
eregion_worker_exits_total
eregion_worker_start_failures_total
eregion_worker_crash_loops_total

eregion_queue_waiting
eregion_queue_rejections_total
eregion_queue_wait_duration_seconds
```

Avoid worker ID as a Prometheus label to prevent unnecessary cardinality. Worker IDs belong in logs.

---

# 18. Logging

Use Go `log/slog` unless a concrete limitation requires another library.

Formats:

- `text`;
- `json`.

Levels:

- `debug`;
- `info`;
- `warn`;
- `error`.

Recommended fields:

- `timestamp`;
- `level`;
- `component`;
- `message`;
- `request_id`;
- `worker_id`;
- `generation`;
- `pid`;
- `method`;
- `path`;
- `status`;
- `duration_ms`;
- `queue_wait_ms`;
- `exit_code`;
- `recycle_reason`;
- `error`.

Do not log by default:

- Authorization;
- cookies;
- secrets;
- request bodies;
- response bodies;
- passwords;
- personal data.

---

# 19. Security and protocol limits

Required controls:

- private socket directory;
- restricted directory and socket permissions;
- generated socket names;
- stale socket cleanup;
- strict MessagePack envelope validation;
- maximum frame size;
- maximum HTTP body size;
- maximum header size;
- bounded queue;
- request and handshake deadlines;
- no command construction from HTTP input;
- no operational debug endpoint enabled by default;
- no silent protocol downgrade.

Default protocol frame limit:

```text
16 MiB
```

The protocol frame limit must be larger than the HTTP body limit because the frame includes headers and metadata.

---

# 20. Eregion project structure

```text
eregion/
├── cmd/
│   └── eregion/
│       └── main.go
│
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   ├── defaults.go
│   │   └── validation.go
│   │
│   ├── server/
│   │   ├── server.go
│   │   ├── middleware.go
│   │   └── operations.go
│   │
│   ├── worker/
│   │   ├── worker.go
│   │   ├── manager.go
│   │   ├── pool.go
│   │   ├── process.go
│   │   ├── state.go
│   │   ├── events.go
│   │   └── recycle.go
│   │
│   ├── dispatcher/
│   │   └── dispatcher.go
│   │
│   ├── protocol/
│   │   ├── frame.go
│   │   ├── handshake.go
│   │   ├── request.go
│   │   ├── response.go
│   │   └── codec.go
│   │
│   ├── socket/
│   │   ├── listener.go
│   │   └── cleanup.go
│   │
│   ├── lifecycle/
│   │   └── shutdown.go
│   │
│   ├── telemetry/
│   │   ├── metrics.go
│   │   └── health.go
│   │
│   └── logging/
│       └── logger.go
│
├── tests/
│   └── fixtures/
│       ├── healthy-worker.php
│       ├── crashing-worker.php
│       ├── slow-worker.php
│       ├── malformed-worker.php
│       └── recycling-worker.php
│
├── eregion.yaml.example
├── Makefile
├── go.mod
├── go.sum
├── README.md
├── LICENSE
└── VERSION
```

Do not add speculative abstractions. Interfaces should exist only at meaningful substitution or testing boundaries.

---

# 21. Core Go interfaces

```go
type Pool interface {
    Start(ctx context.Context) error
    Acquire(ctx context.Context) (*Worker, error)
    Release(worker *Worker)
    Discard(worker *Worker, reason error)
    Shutdown(ctx context.Context) error
    Snapshot() PoolSnapshot
}
```

```go
type WorkerClient interface {
    Send(
        ctx context.Context,
        worker *Worker,
        request RequestEnvelope,
    ) (ResponseEnvelope, error)
}
```

```go
type ProcessSpawner interface {
    Spawn(
        ctx context.Context,
        specification ProcessSpecification,
    ) (*os.Process, error)
}
```

Avoid interfaces around simple internal structs without multiple implementations or a testing boundary.

---

# 22. MithrilPHP current capabilities found

The current MithrilPHP runtime already establishes the correct foundation:

- `HttpApplication`-based kernel execution.
- `Worker` that boots the application once.
- `RequestBridge` abstraction.
- `beginScope()` before each request.
- `endScope()` in `finally`.
- `maxRequests` support.
- loop termination after the configured request count.
- compiled-container warm execution path.
- documented `resetWorker()` capability.
- FPM one-request bridge.
- in-memory bridge for tests.

Current conceptual worker flow:

```text
app.boot() once
    ↓
loop
    ↓
bridge.next()
    ↓
container.beginScope()
    ↓
app.handle()
    ↓
bridge.respond()
    ↓
container.endScope()
    ↓
stop after maxRequests when configured
```

This foundation must be evolved rather than replaced.

---

# 23. Required MithrilPHP implementation

This section defines the library work needed for Eregion integration.

## 23.1 Eregion bridge

Add an official bridge implementation:

```text
src/Runtime/Eregion/EregionBridge.php
```

Responsibilities:

- connect or accept the configured UDS according to the final direction;
- complete EREGION protocol handshake;
- read length-prefixed frames exactly;
- validate frame length;
- call `msgpack_unpack`;
- validate request envelopes;
- map envelopes into MithrilPHP `Request`;
- map MithrilPHP `Response` into response envelopes;
- attach worker metadata;
- call `msgpack_pack`;
- write complete frames;
- recognize shutdown or drain messages;
- throw typed protocol exceptions.

The bridge must not call `beginScope()` or `endScope()`. The existing MithrilPHP `Worker` owns request scopes.

## 23.2 Request and response mappers

Add:

```text
src/Runtime/Eregion/RequestMapper.php
src/Runtime/Eregion/ResponseMapper.php
```

Requirements:

- preserve repeated headers;
- preserve binary body;
- preserve query string;
- preserve remote address, host, scheme, and protocol;
- validate HTTP status;
- avoid normalizing away `Set-Cookie`;
- avoid body Base64 encoding.

## 23.3 Frame reader and writer

Add:

```text
src/Runtime/Eregion/FrameReader.php
src/Runtime/Eregion/FrameWriter.php
```

Requirements:

- read exactly four header bytes;
- decode big-endian length;
- enforce `max_frame_bytes`;
- read exactly N payload bytes;
- detect EOF and partial frames;
- handle partial writes;
- use typed protocol exceptions;
- never allocate based on an unchecked frame length.

## 23.4 Protocol types and exceptions

Add typed representations or value objects for:

- `HelloMessage`;
- `ReadyMessage`;
- `RequestEnvelope`;
- `ResponseEnvelope`;
- `ResponseMetadata`;
- `ProtocolError`;
- `ShutdownMessage`.

Add exceptions:

```text
ProtocolException
InvalidFrameException
FrameTooLargeException
HandshakeException
UnsupportedProtocolVersionException
UnexpectedMessageException
ConnectionClosedException
```

## 23.5 Generic worker entrypoint

MithrilPHP must provide a generic worker launcher:

```text
bin/eregion-worker
```

The client application should not need to create a custom worker script.

The launcher must:

1. Parse CLI arguments.
2. Load runtime manifest.
3. Require Composer autoload.
4. Instantiate the configured kernel.
5. Validate `HttpApplication`.
6. Create `EregionBridge`.
7. Create recycling policies.
8. Run the MithrilPHP `Worker`.
9. Return a meaningful exit code.

## 23.6 Runtime manifest

Forge should generate:

```text
var/runtime/eregion.json
```

Example:

```json
{
  "application": "App\\Kernel",
  "autoload": "/app/vendor/autoload.php",
  "workingDirectory": "/app",
  "compiledContainer": "/app/var/cache/container.php",
  "compiledRoutes": "/app/var/cache/routes.php",
  "environment": "production",
  "worker": {
    "maxRequests": 1000,
    "memoryLimitBytes": 268435456
  },
  "protocol": {
    "version": 1,
    "maxFrameBytes": 16777216
  }
}
```

Eregion knows how to run processes. MithrilPHP knows how to construct the application.

## 23.7 Forge `serve` command

Add or evolve:

```bash
vendor/bin/forge serve
```

Responsibilities:

- resolve application kernel;
- validate application contract;
- validate PHP version;
- validate `ext-msgpack`;
- validate Eregion configuration;
- compile or verify container/routes;
- generate runtime manifest;
- locate Eregion binary;
- pass config and manifest paths;
- replace the current process with Eregion;
- preserve the exit code.

The command must not implement the HTTP server in PHP.

## 23.8 Forge environment checks

Add:

```bash
vendor/bin/forge server:check
```

Checks:

- PHP version;
- `ext-msgpack`;
- MithrilPHP compatibility;
- kernel class;
- compiled artifact readability;
- worker entrypoint;
- socket directory writability;
- UDS availability;
- Eregion binary;
- Eregion protocol compatibility;
- configuration validity;
- bind port availability when useful.

## 23.9 Binary resolution and installation

Resolution priority:

1. `EREGION_BINARY` environment variable.
2. `eregion` in `PATH`.
3. project-local `.mithril/bin/eregion`.
4. explicit installation workflow.

Add:

```bash
vendor/bin/forge server:install
```

Installation must:

- detect platform and architecture;
- download a pinned compatible version;
- verify checksum;
- set executable permissions;
- avoid unexpected interactive downloads in production.

---

# 24. MithrilPHP recycling implementation

## 24.1 Current behavior

The current Worker already:

- counts requests;
- accepts `maxRequests`;
- breaks the request loop after reaching the limit;
- calls `endScope()` in a `finally` block.

This provides basic request-count recycling and request-scope cleanup.

## 24.2 Missing capabilities

Implement:

- structured worker result;
- worker stop reason;
- PHP memory reporting;
- memory-limit recycling;
- cooperative recycling decision;
- recycling reason;
- response metadata;
- planned worker exit code;
- explicit shutdown/drain support;
- scope-cleanup failure handling;
- protocol failure exit handling.

## 24.3 Recycling policy contract

```php
interface RecyclingPolicy
{
    public function evaluate(
        WorkerContext $context
    ): RecyclingDecision;
}
```

```php
final readonly class WorkerContext
{
    public function __construct(
        public int $requestsHandled,
        public int $memoryUsage,
        public int $memoryPeak,
        public ?Throwable $lastError,
    ) {}
}
```

```php
final readonly class RecyclingDecision
{
    public function __construct(
        public bool $shouldRecycle,
        public ?string $reason = null,
    ) {}

    public static function keepAlive(): self
    {
        return new self(false);
    }

    public static function recycle(string $reason): self
    {
        return new self(true, $reason);
    }
}
```

## 24.4 Initial policies

### Max requests

```php
final class MaxRequestsPolicy implements RecyclingPolicy
{
    public function __construct(
        private readonly int $maximum,
    ) {}

    public function evaluate(WorkerContext $context): RecyclingDecision
    {
        if (
            $this->maximum > 0
            && $context->requestsHandled >= $this->maximum
        ) {
            return RecyclingDecision::recycle('max_requests');
        }

        return RecyclingDecision::keepAlive();
    }
}
```

### Memory limit

```php
final class MemoryLimitPolicy implements RecyclingPolicy
{
    public function __construct(
        private readonly int $maximumBytes,
    ) {}

    public function evaluate(WorkerContext $context): RecyclingDecision
    {
        if (
            $this->maximumBytes > 0
            && $context->memoryUsage >= $this->maximumBytes
        ) {
            return RecyclingDecision::recycle('memory_limit');
        }

        return RecyclingDecision::keepAlive();
    }
}
```

### Composite policy

```php
final class CompositeRecyclingPolicy implements RecyclingPolicy
{
    /** @param list<RecyclingPolicy> $policies */
    public function __construct(
        private readonly array $policies,
    ) {}

    public function evaluate(WorkerContext $context): RecyclingDecision
    {
        foreach ($this->policies as $policy) {
            $decision = $policy->evaluate($context);

            if ($decision->shouldRecycle) {
                return $decision;
            }
        }

        return RecyclingDecision::keepAlive();
    }
}
```

## 24.5 Worker metadata

Add:

```php
final readonly class WorkerMetadata
{
    public function __construct(
        public int $requestsHandled,
        public int $memoryUsage,
        public int $memoryPeak,
        public bool $recycle,
        public ?string $recycleReason,
    ) {}
}
```

This metadata is included in the protocol response.

## 24.6 Structured worker result

Change `Worker::run()` from returning only an integer to returning a structured result, or add a compatible evolved API.

```php
final readonly class WorkerResult
{
    public function __construct(
        public int $requestsHandled,
        public WorkerStopReason $reason,
        public ?string $detail = null,
    ) {}
}
```

```php
enum WorkerStopReason: string
{
    case Stopped = 'stopped';
    case Recycled = 'recycled';
    case RemoteShutdown = 'remote_shutdown';
    case ProtocolFailure = 'protocol_failure';
    case ScopeCleanupFailure = 'scope_cleanup_failure';
    case BootstrapFailure = 'bootstrap_failure';
}
```

Backward compatibility must be considered if `Worker::run(): int` is public and already consumed. Options:

- release as a new major version;
- add `runResult()` and keep `run()` as compatibility wrapper;
- introduce a new `PersistentWorker` class and deprecate the old return contract.

The agent must inspect usage before choosing.

## 24.7 Recommended worker loop

The loop must:

1. Boot once.
2. Receive request.
3. Open scope.
4. Execute application.
5. Close scope.
6. Measure memory.
7. Evaluate recycling.
8. Send response and metadata.
9. Exit only after the final response has been sent.

Important ordering:

```text
handle request
→ end request scope
→ evaluate/report recycling
→ send valid response
→ exit loop when recycle=true
```

The implementation must define how response metadata is passed without breaking generic `RequestBridge`. Recommended options:

### Option A — Eregion-aware bridge capability

```php
interface WorkerMetadataAwareBridge extends RequestBridge
{
    public function respond(
        Response $response,
        WorkerMetadata $metadata
    ): void;
}
```

The Worker checks capability:

```php
if ($this->bridge instanceof WorkerMetadataAwareBridge) {
    $this->bridge->respond($response, $metadata);
} else {
    $this->bridge->respond($response);
}
```

### Option B — Response context method

```php
$this->bridge->setWorkerMetadata($metadata);
$this->bridge->respond($response);
```

Option A is preferred because it is explicit and avoids mutable bridge state.

## 24.8 Scope cleanup failure

`endScope()` currently runs in `finally`, which is correct. However, if `endScope()` itself fails:

- the worker state is no longer trustworthy;
- do not process another request;
- attempt to return a safe response only when possible;
- exit with `ScopeCleanupFailure`;
- let Eregion replace the process.

## 24.9 Memory measurement

Use:

```php
memory_get_usage(true)
memory_get_peak_usage(true)
```

These are portable and sufficient for the first cooperative policy.

Eregion may later add Linux RSS monitoring, but `/proc` polling is outside the MVP.

## 24.10 `resetWorker()` policy

Do not use `resetWorker()` as a replacement for process recycling in v1.

Recommended v1 behavior:

```text
every request:
  beginScope/endScope

max requests or memory limit:
  recycle entire PHP process
```

Reason: the container cannot reliably reset arbitrary static state, native extension state, global caches, or resources created outside the container.

`resetWorker()` may remain an explicit advanced API, but Eregion should prefer complete process replacement.

## 24.11 Exit codes

Recommended worker exit codes:

```php
enum WorkerExitCode: int
{
    case Normal = 0;
    case Recycled = 10;
    case BootstrapFailure = 20;
    case ProtocolFailure = 21;
    case ScopeCleanupFailure = 22;
}
```

Eregion interpretation:

```text
0  → normal requested stop
10 → planned recycle; replace without crash penalty
20+ → failure; count and apply restart policy
signal/unknown → crash
```

The final mapping should be documented and tested.

## 24.12 Remote shutdown

The Eregion bridge should recognize a protocol shutdown message.

Behavior:

- stop accepting another request;
- return a structured `RemoteShutdown` result;
- exit normally;
- avoid counting as crash.

OS signals remain the fallback and forced-shutdown mechanism.

---

# 25. MithrilPHP proposed file structure

```text
mithrilphp/
├── src/
│   └── Runtime/
│       ├── Worker.php
│       ├── WorkerResult.php
│       ├── WorkerStopReason.php
│       ├── WorkerMetadata.php
│       │
│       ├── Recycling/
│       │   ├── RecyclingPolicy.php
│       │   ├── RecyclingDecision.php
│       │   ├── WorkerContext.php
│       │   ├── MaxRequestsPolicy.php
│       │   ├── MemoryLimitPolicy.php
│       │   └── CompositeRecyclingPolicy.php
│       │
│       └── Eregion/
│           ├── EregionBridge.php
│           ├── WorkerMetadataAwareBridge.php
│           ├── FrameReader.php
│           ├── FrameWriter.php
│           ├── RequestMapper.php
│           ├── ResponseMapper.php
│           ├── Protocol.php
│           ├── Messages/
│           └── Exceptions/
│
├── bin/
│   ├── forge
│   └── eregion-worker
│
└── tests/
    ├── Unit/
    │   └── Runtime/
    └── Integration/
        └── Eregion/
```

Keep Eregion integration cohesive. Do not leak MessagePack concerns throughout the core HTTP domain.

---

# 26. Tests

## 26.1 Eregion Go unit tests

Cover:

- configuration defaults;
- strict config validation;
- duration parsing;
- permissions parsing;
- frame read/write;
- partial reads and writes;
- oversized frame rejection;
- MessagePack envelope validation;
- handshake compatibility;
- request ID correlation;
- worker state transitions;
- stale-generation event rejection;
- acquire/release behavior;
- queue full;
- acquire timeout;
- request timeout;
- restart backoff;
- restart-window limit;
- planned recycle;
- crash versus recycle accounting;
- graceful shutdown;
- socket cleanup.

## 26.2 Go race tests

Required in CI:

```bash
go test -race ./...
```

No goroutine may lack an explicit shutdown path.

## 26.3 Go ↔ PHP integration fixtures

Create fixtures:

- healthy worker;
- slow worker;
- crashing worker;
- malformed MessagePack worker;
- oversized-frame worker;
- mismatched-request-ID worker;
- startup-failure worker;
- recycling worker;
- scope-cleanup-failure worker.

Validate:

1. basic GET;
2. JSON body;
3. binary body;
4. repeated headers;
5. empty response;
6. response body bytes;
7. worker crash while idle;
8. worker crash while busy;
9. worker request timeout;
10. handshake timeout;
11. protocol version mismatch;
12. worker replacement;
13. fixed pool concurrency;
14. bounded queue;
15. client cancellation;
16. graceful shutdown;
17. planned max-request recycle;
18. memory recycle;
19. no automatic request replay.

## 26.4 MithrilPHP unit tests

Cover:

- `MaxRequestsPolicy`;
- `MemoryLimitPolicy`;
- composite policy ordering;
- `RecyclingDecision`;
- `WorkerResult`;
- response metadata;
- exit-code mapping;
- frame reader/writer;
- protocol exception types;
- request mapper;
- response mapper;
- shutdown message handling.

## 26.5 MithrilPHP lifecycle integration tests

Validate:

- kernel boot runs once per worker;
- singleton persists within worker lifetime;
- scoped service is recreated for each request;
- previous request state does not leak;
- `endScope()` runs after application exception;
- worker exits after sending the last max-request response;
- worker exits after memory decision;
- recycle metadata contains reason;
- `500` application response does not recycle by default;
- malformed protocol causes worker failure;
- scope cleanup failure stops worker;
- remote shutdown exits normally;
- binary body remains unchanged;
- repeated `Set-Cookie` headers remain unchanged.

## 26.6 Load benchmark

Compare:

- classic one-shot path;
- Eregion with 1 worker;
- Eregion with 4 workers;
- Eregion with 8 workers.

Measure:

- requests per second;
- p50;
- p95;
- p99;
- queue wait;
- worker memory;
- error rate;
- startup time;
- recycle interruption behavior.

Do not publish performance claims without reproducible benchmark scripts and environment details.

---

# 27. MVP acceptance criteria

The Eregion MVP is complete when:

- `vendor/bin/forge serve` starts the server.
- Forge generates a runtime manifest.
- Forge validates `ext-msgpack`.
- Eregion starts a configured fixed PHP worker pool.
- Every PHP worker boots MithrilPHP once.
- Each worker processes one request at a time.
- IPC uses persistent UDS.
- IPC uses length-prefixed MessagePack.
- Handshake validates protocol version and generation.
- Requests and binary bodies round-trip correctly.
- Request scopes open and close per request.
- Workers are recycled after `max_requests`.
- Workers are recycled after PHP memory threshold.
- Planned recycle is distinguished from crash.
- Dead workers are detected and replaced.
- Timed-out workers are terminated and replaced.
- Restart loops use backoff and limits.
- Queue capacity is bounded.
- Saturation returns `503`.
- Worker timeout returns `504`.
- Worker death/protocol failure returns `502`.
- Requests are not automatically replayed.
- Liveness, readiness, health, and metrics work.
- SIGTERM performs graceful shutdown.
- UDS files are removed.
- Unit, integration, and race tests pass.

---

# 28. Out of scope for MVP

Do not implement initially:

- dynamic worker autoscaling;
- runtime hot configuration;
- WebSocket;
- SSE;
- HTTP/3;
- TLS termination;
- multi-application hosting;
- Laravel/Symfony adapters;
- Windows-native named-pipe support;
- distributed workers;
- multi-node balancing;
- dashboard;
- plugin system;
- automatic code reload;
- file watcher;
- MessagePack/JSON codec selection;
- protocol streaming frames;
- external RSS polling;
- Kubernetes API integration.

---

# 29. Implementation phases

## Phase 1 — Protocol foundation

Eregion:

- frame codec;
- MessagePack envelopes;
- strict validation;
- handshake;
- PHP fixtures.

MithrilPHP:

- frame reader/writer;
- protocol messages;
- typed exceptions;
- initial Eregion bridge skeleton.

## Phase 2 — Single persistent worker

- Go HTTP server;
- one PHP process;
- persistent UDS;
- request/response round trip;
- timeouts;
- binary body;
- basic shutdown.

## Phase 3 — MithrilPHP lifecycle integration

- generic worker entrypoint;
- runtime manifest;
- request/response mappers;
- Forge environment checks;
- `forge serve`;
- scope isolation tests.

## Phase 4 — Fixed pool and backpressure

- worker states;
- acquire/release;
- bounded queue;
- one request per worker;
- concurrency tests;
- race detector.

## Phase 5 — Supervision

- process monitor;
- idle/busy death detection;
- generation handling;
- replacement;
- restart backoff;
- crash-loop protection;
- startup timeout.

## Phase 6 — Recycling

MithrilPHP:

- recycling policies;
- memory metadata;
- structured worker result;
- planned exit codes;
- response metadata.

Eregion:

- draining state;
- planned recycle handling;
- replacement without crash penalty;
- recycle metrics.

## Phase 7 — Operations

- logging;
- access logs;
- health;
- readiness;
- liveness;
- Prometheus metrics;
- complete graceful shutdown.

## Phase 8 — Distribution

- Linux AMD64;
- Linux ARM64;
- macOS AMD64;
- macOS ARM64;
- checksums;
- GitHub releases;
- Docker example;
- Forge installer.

---

# 30. Initial ADRs

## ADR-001 — Go for the server

Use Go for HTTP, concurrency, process management, supervision, and the distributable binary.

## ADR-002 — MithrilPHP owns PHP lifecycle

The existing MithrilPHP Worker remains responsible for boot, request scope, kernel execution, and PHP-side recycling decisions.

## ADR-003 — UDS and MessagePack

Use persistent Unix Domain Sockets, 4-byte big-endian framing, and MessagePack.

## ADR-004 — Fixed worker pool

Use a fixed configured worker count in v1. Dynamic scaling belongs to Narya.

## ADR-005 — One request per worker

Do not run concurrent requests inside a single PHP process.

## ADR-006 — Bounded queue

Never use an unlimited request queue.

## ADR-007 — No automatic replay

Do not retry a request automatically after worker death or timeout.

## ADR-008 — Process replacement over container reset

Use `beginScope/endScope` every request and recycle the entire PHP process at max requests or memory threshold. Do not rely on `resetWorker()` as the primary safety mechanism.

## ADR-009 — Forge is the public entrypoint

MithrilPHP users start Eregion through `vendor/bin/forge serve`.

---

# 31. Rules for the implementation agent

Before adding a feature, evaluate:

1. Is it necessary for the MVP?
2. Does MithrilPHP already own this responsibility?
3. Does it belong to Eregion or Narya?
4. Can the Go standard library solve it?
5. Is the abstraction needed now?
6. Can the behavior be tested?
7. Is state safe under concurrency?
8. How does it shut down?
9. What happens when PHP crashes?
10. What happens when IPC becomes invalid?
11. Can request state leak?
12. Does this option have actual behavior?
13. Is compatibility with the existing MithrilPHP public API preserved?

Mandatory engineering rules:

- prefer standard library;
- keep dependencies minimal;
- use `context.Context`;
- never create unbounded channels or queues;
- never start unmanaged goroutines;
- centralize worker state transitions;
- use worker generations;
- validate all frame lengths before allocation;
- distinguish planned recycle from crash;
- never silently downgrade protocol;
- never replay uncertain requests;
- write tests with implementation;
- run race detector;
- document architectural changes;
- avoid speculative abstractions;
- inspect existing usages before breaking public MithrilPHP APIs.

---

# 32. Final product definition

Eregion is the specialized Go application server for MithrilPHP.

It keeps PHP applications warm through persistent, isolated workers while handling HTTP, MessagePack IPC, process supervision, bounded backpressure, timeouts, worker replacement, cooperative recycling, observability, and graceful shutdown.

MithrilPHP remains the execution core. Eregion keeps the forge alive.

> **Build with Mithril. Run in Eregion.**
