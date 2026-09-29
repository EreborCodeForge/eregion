# Eregion — Dynamic Workloads & Consumer Supervision

**Status:** DONE (Phase 3 workloads baseline); Phase 4 scaling **PARTIAL** in master spec  
**Repository:** `EreborCodeForge/eregion`  
**Integração transversal:** [`durin-architecture` — workloads master spec](https://github.com/EreborCodeForge/durin-architecture/blob/main/docs/specs/durin-workloads-integration-master-spec.md)

# Mission

Evoluir o Eregion de “servidor HTTP com um único pool de PHP workers” para **supervisor de workloads**.

HTTP passa a ser um tipo de workload. O primeiro conjunto suportado deve ser:

```text
http
consumer
```

Cada workload possui configuração própria:

```text
name
template
mode
command
queue
workers
resources
scaling
```

O Eregion deve supervisionar processos, escalar capacidade e expor saúde/métricas. Ele **não** deve assumir a responsabilidade de consumir MQTT/SQS/RabbitMQ diretamente nesta fase.

# Current State

Hoje o Eregion já possui boa parte da infraestrutura reutilizável:

- pool persistente de processos PHP;
- restart e exponential backoff;
- worker generations;
- graceful shutdown;
- health/readiness/liveness;
- métricas;
- sizing de CPU/memória;
- limites de memória;
- fila de espera para requisições HTTP;
- UDS + MessagePack para o workload HTTP.

A `queue.capacity` atual é fila de espera de **HTTP**, não fila de jobs.

# Target Architecture

```text
                     Eregion
                        │
                 WorkloadRegistry
                        │
                    Reconciler
                        │
       ┌────────────────┼────────────────┐
       ▼                ▼                ▼
 HTTP workload    Consumer workload  Consumer workload
       │                │                │
  WorkerPool        WorkerPool        WorkerPool
       │                │                │
 EREGION/1        job-worker         job-worker
```

# Workload Configuration

```yaml
workload_templates:
  io-consumer:
    mode: consumer
    workers:
      min: 1
      max: 8
    resources:
      class: io
      memory_mb: 128
    scaling:
      strategy: backlog
      scale_up_cooldown: 1s
      scale_down_idle_for: 30s

  cpu-heavy:
    mode: consumer
    workers:
      min: 0
      max: 4
    resources:
      class: cpu
      memory_mb: 1024

workloads:
  telemetry:
    template: io-consumer
    command:
      - php
      - vendor/bin/job-worker
      - --kernel=App\\TelemetryKernel
    queue:
      transport: mqtt
      name: devices/+/temperature

  video-transcode:
    template: cpu-heavy
    command:
      - php
      - vendor/bin/job-worker
      - --kernel=App\\VideoTranscodeKernel
    queue:
      transport: sqs
      name: video.transcode
    workers:
      max: 6
```

`command` deve ser uma lista argv. Não usar string interpretada por shell.

# Workload Model

Criar estruturas equivalentes a:

```go
type WorkloadMode string

const (
    WorkloadHTTP     WorkloadMode = "http"
    WorkloadConsumer WorkloadMode = "consumer"
)

type WorkloadSpec struct {
    Name      string
    Template  string
    Mode      WorkloadMode
    Command   []string
    Queue     QueueMetadata
    Workers   WorkerPolicy
    Resources ResourcePolicy
    Scaling   ScalingPolicy
}
```

Também separar:

```text
WorkloadTemplate
WorkloadSpec
ResolvedWorkloadSpec
```

O scheduler/reconciler recebe apenas `ResolvedWorkloadSpec`.

# Templates

Templates descrevem comportamento operacional.

Bons exemplos:

```text
io-consumer
cpu-heavy
burst
always-on
latency-sensitive
batch
```

Evitar templates de domínio como `email-worker` ou `payment-worker`.

Overrides do workload devem ter precedência sobre o template.

# WorkloadRegistry

O registry é a fonte de estado desejado dentro do Eregion.

API mínima:

```text
Create / Upsert
Update
Remove
Get
List
```

YAML é uma fonte de configuração, não a representação interna obrigatória.

No futuro CLI/API podem criar workloads dinamicamente sem alterar a arquitetura.

# Reconciler

Adicionar loop de reconciliação:

```text
desired state
    vs
current state
    ↓
start / drain / restart
```

Exemplo:

```text
desired workers = 6
current workers = 3
→ start 3
```

```text
desired workers = 2
current workers = 6
→ drain 4
```

Worker nenhum deve criar outro worker diretamente.

# Multiple Worker Pools

O atual pool único precisa virar pool por workload.

```text
PoolManager
  ├── http → WorkerPool
  ├── telemetry → WorkerPool
  └── transcode → WorkerPool
```

Cada pool possui isoladamente:

```text
slots
generations
restart policy
backoff
draining
worker states
metrics
```

Falha em um workload não deve corromper outro.

# Consumer Mode

`consumer` deve:

- não abrir listener HTTP da aplicação;
- executar o `command` configurado N vezes;
- aceitar `workers.min = 0`;
- reiniciar crashes;
- aplicar backoff;
- fazer graceful drain;
- respeitar limites de CPU/memória;
- manter métricas e health operacional.

Fluxo:

```text
Eregion
  ↓
php vendor/bin/job-worker
  ↓
Mithril JobWorker
  ↓
JobTransport
  ↓
MQTT / SQS / Rabbit / Redis
```

O broker continua sendo consumido pelo PHP/Mithril.

# HTTP Mode

Preservar o comportamento atual como workload HTTP:

```text
HTTP listener
  ↓
Dispatcher
  ↓
WorkerPool
  ↓
EREGION/1
  ↓
Mithril HTTP Worker
```

Não quebrar `eregion/1` para introduzir consumer workloads.

# Scaling

Separar a política de scaling do pool.

Contrato conceitual:

```go
type ScalingStrategy interface {
    DesiredWorkers(
        spec ResolvedWorkloadSpec,
        metrics WorkloadMetrics,
        resources RuntimeResources,
    ) int
}
```

Primeiras estratégias:

```text
fixed
backlog
resources
```

Para backlog:

```text
desired ≈ ceil(
    backlog * avg_job_duration
    --------------------------
       target_drain_time
)
```

Sempre aplicar clamp:

```text
workers.min
workers.max
CPU disponível
memória disponível
```

CPU isoladamente não deve determinar scaling.

# Scale Up / Down

Aplicar histerese:

```text
scale-up rápido
scale-down lento
```

Direção inicial:

```text
scale_up_cooldown: ~1s
scale_down_idle_for: 30–60s
```

O scale-down deve fazer drain; não matar worker ocupado.

# Graceful Drain

Estados mínimos:

```text
starting
idle
busy
draining
stopped
failed
```

Drain:

```text
mark draining
↓
SIGTERM
↓
worker termina unidade atual
↓
espera shutdown timeout
↓
force kill somente se necessário
```

# Backlog

Não duplicar a fila do broker dentro do Eregion.

Correto:

```text
Broker = backlog
Eregion = capacidade
Mithril = execução
```

Para scaling baseado em backlog, introduzir uma abstração futura `BacklogProvider`. Primeira versão pode usar fixed scaling ou métrica externa.

# Resource Awareness

Reutilizar o detector atual de CPU/cgroup/memória.

Permitir resource class:

```text
io
cpu
balanced
```

A classe influencia recomendações e limites, nunca substitui os hard limits configurados.

# Observability

Adicionar labels por workload:

```text
eregion_workload_desired_workers{workload=...}
eregion_workload_running_workers{workload=...}
eregion_workload_busy_workers{workload=...}
eregion_workload_draining_workers{workload=...}
eregion_workload_failed_workers{workload=...}
eregion_workload_restarts_total{workload=...}
eregion_workload_scale_events_total{workload=...,direction=...}
```

# Backwards Compatibility

A configuração HTTP atual deve continuar funcionando.

Criar adapter:

```text
legacy config
   ↓
LegacyConfigAdapter
   ↓
ResolvedWorkloadSpec(name=http)
```

Não manter duas implementações diferentes do runtime HTTP.

# Must Do

- workloads dinâmicos;
- templates;
- múltiplos pools;
- `http` e `consumer`;
- reconciler;
- min/max;
- `min=0`;
- scaling extensível;
- graceful drain;
- métricas por workload;
- compatibilidade com configuração HTTP atual;
- comando em argv array.

# Must Not

- implementar broker obrigatório em Go;
- transformar `queue.capacity` HTTP em job queue;
- deixar worker criar sibling worker;
- usar shell interpolation;
- compartilhar um único pool entre workloads;
- matar job ativo em scale-down normal;
- quebrar EREGION/1 sem necessidade.

# Tests

Cobrir:

```text
legacy HTTP → workload HTTP
2+ workloads independentes
template + overrides
min/max
consumer min=0
scale up
scale down + cooldown
drain
restart/backoff
resource clamp
isolamento entre pools
metrics labels
shutdown global
```

# Acceptance Criteria

- Eregion executa HTTP e consumer workloads simultaneamente.
- Consumer executa `php vendor/bin/job-worker`.
- Cada workload possui pool e limites próprios.
- Worker count pode mudar sem reiniciar Eregion.
- Broker client não é requisito do Eregion.
- HTTP atual continua funcional.
- Scale-down é graceful.

# Definition of Done

Eregion passa a ser um **workload supervisor**, onde HTTP é apenas um dos modos disponíveis.
