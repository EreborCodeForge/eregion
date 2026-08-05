# MithrilPHP × Eregion — Spec de implementação PHP

**Status:** Draft for implementation  
**Versão:** 1.0  
**Data:** 2026-08-03  
**Repo alvo:** `EreborCodeForge/mithrilphp`  
**Contrato irmão:** [eregion-application-server-spec.md](eregion-application-server-spec.md)  
**Config canônica do servidor:** [eregion.yaml](eregion.yaml)

---

## 1. Objetivo

Este documento define **somente o que a biblioteca PHP (MithrilPHP + Forge)** deve implementar para integrar com o Eregion Application Server (Go).

O Eregion já existe neste repositório. Ele:

- aceita HTTP;
- supervisiona processos PHP;
- fala UDS + MessagePack length-prefixed;
- aplica fila, timeouts e recycle planejado.

A lib PHP **não** implementa servidor HTTP. Ela implementa o **worker persistente** e a **CLI Forge** que prepara e inicia o Eregion.

---

## 2. Fronteira de responsabilidade

### 2.1 MithrilPHP / Forge deve

| Área | Responsabilidade |
|------|------------------|
| Boot | Resolver kernel, autoload, container/routes compilados |
| IPC | Criar UDS, handshake `hello`→`ready`, frames MessagePack |
| HTTP mapping | Envelope ↔ `Request` / `Response` Mithril |
| Escopo | `beginScope()` / `endScope()` por request (Worker existente) |
| Recycle | Políticas cooperativas + metadata na resposta |
| Exit | Códigos de saída semânticos |
| DX | `forge serve`, `forge server:check`, `forge server:install` |
| Manifest | Gerar `var/runtime/eregion.json` |

### 2.2 MithrilPHP / Forge não deve

- Implementar servidor HTTP/TCP público
- Gerenciar pool de workers
- Aplicar backoff/crash-loop (isso é Go)
- Autoscaling, WebSocket, SSE, TLS termination
- Fallback silencioso para JSON em vez de MessagePack
- Usar `resetWorker()` como substituto de recycle de processo no v1

### 2.3 Aplicação do usuário deve

- Implementar `HttpApplication`
- Evitar estado de request em globais/singletons
- Registrar estado por request como scoped
- Boot idempotente
- Tratar exceções de aplicação no kernel (HTTP 500 válido ≠ falha de protocolo)

---

## 3. Fluxo ponta a ponta

```text
composer install
    ↓
vendor/bin/forge serve
    ↓
validar PHP, ext-msgpack, kernel, artifacts
    ↓
gerar var/runtime/eregion.json
    ↓
localizar binário eregion
    ↓
exec(eregion serve --config=... --manifest=...)
    ↓
Eregion sobe N workers:
  php vendor/.../bin/eregion-worker \
    --socket=/tmp/eregion/worker-1-1.sock \
    --worker-id=worker-1 \
    --generation=1 \
    --max-requests=1000 \
    --memory-limit-mb=256 \
    --manifest=/app/var/runtime/eregion.json
    ↓
PHP cria UDS, aceita conexão, handshake, loop de requests
```

**Direção do socket (v1, alinhada ao Eregion Go atual):**

1. Eregion escolhe o path `worker-{slot}-{generation}.sock`
2. Eregion inicia o processo PHP com `--socket=...`
3. **PHP cria e escuta** o UDS
4. Eregion conecta, envia `hello`, PHP responde `ready`
5. Conexão permanece aberta pela vida dessa generation

---

## 4. Requisitos de runtime PHP

| Requisito | Notas |
|-----------|--------|
| PHP 8.3+ (recomendado) | Alinhar com check do Forge |
| `ext-msgpack` | Obrigatório; sem fallback JSON |
| `ext-sockets` | Para `AF_UNIX` |
| Sistema Unix | Linux/macOS; Windows nativo fora do MVP da lib (mesmo que testes locais com AF_UNIX existam) |

Checagens obrigatórias em `forge serve` / `forge server:check`:

- versão PHP;
- `extension_loaded('msgpack')`;
- `extension_loaded('sockets')` ou equivalente utilizável;
- classe kernel carregável e `HttpApplication`;
- artifacts compilados legíveis;
- entrypoint `eregion-worker` presente;
- binário Eregion resolvível.

---

## 5. CLI do worker — `bin/eregion-worker`

### 5.1 Invocação (contrato com Eregion Go)

```bash
php bin/eregion-worker \
  --socket=/tmp/eregion/worker-2-8.sock \
  --worker-id=worker-2 \
  --generation=8 \
  --max-requests=1000 \
  --memory-limit-mb=256 \
  --manifest=/app/var/runtime/eregion.json
```

| Flag | Tipo | Obrigatório | Semântica |
|------|------|-------------|-----------|
| `--socket` | path | sim | Path UDS a criar/escutar |
| `--worker-id` | string | sim | Ex.: `worker-2`; ecoar no `ready` |
| `--generation` | uint | sim | Generation atual; ecoar no `ready` |
| `--max-requests` | int | sim | `0` = desativado |
| `--memory-limit-mb` | int | sim | `0` = desativado; medir com `memory_get_usage(true)` |
| `--manifest` | path | sim em produção | JSON de boot da aplicação |

A aplicação cliente **não** deve precisar escrever um worker custom.

### 5.2 Sequência do launcher

1. Parsear flags CLI (falha → exit `20`)
2. Carregar manifest JSON
3. `chdir(workingDirectory)` se definido
4. `require` autoload do Composer
5. Instanciar kernel / `HttpApplication`
6. Criar políticas de recycle (`MaxRequests`, `MemoryLimit`, composite)
7. Criar `EregionBridge` (UDS + frames + mappers)
8. Rodar `Worker` Mithril (loop)
9. Sair com código semântico (§12)

### 5.3 Manifest — `var/runtime/eregion.json`

Gerado pelo Forge. Exemplo canônico:

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

Regras:

- Quem **gera**: Forge (`vendor/bin/forge serve`)
- Quem **consome**: `eregion-worker` (PHP)
- Quem **apenas repassa o path**: Eregion Go (`--manifest=...`)
- Paths devem ser absolutos ou resolvíveis a partir de `workingDirectory`
- Schema inválido / arquivo ausente → bootstrap failure (exit `20`)

Prioridade de limites de recycle:

1. Flags CLI do Eregion (`--max-requests`, `--memory-limit-mb`) são a fonte operacional ativa
2. Manifest pode trazer defaults; se ambos existirem, **CLI vence**

---

## 6. Protocolo IPC (obrigatório no PHP)

### 6.1 Framing

```text
┌────────────────────────┬──────────────────────────────┐
│ uint32 big-endian      │ MessagePack payload          │
│ payload byte length    │ exatamente N bytes           │
└────────────────────────┴──────────────────────────────┘
```

Regras do `FrameReader` / `FrameWriter`:

- ler exatamente 4 bytes de header;
- rejeitar `length == 0`;
- rejeitar `length > maxFrameBytes` **antes** de alocar;
- ler exatamente N bytes do payload;
- detectar EOF / frame parcial;
- writes devem lidar com partial write (`socket_write` em loop);
- codec: **somente** MessagePack (`msgpack_pack` / `msgpack_unpack`);
- nunca fazer fallback silencioso para JSON.

### 6.2 Handshake

**Go → PHP (`hello`):**

| Campo | Tipo | Exemplo |
|-------|------|---------|
| `type` | string | `hello` |
| `protocol` | string | `eregion` |
| `protocol_version` | int | `1` |
| `runtime_version` | string | `0.1.0` |
| `worker_id` | string | `worker-2` |
| `generation` | int | `8` |

**PHP → Go (`ready`):**

| Campo | Tipo | Exemplo |
|-------|------|---------|
| `type` | string | `ready` |
| `protocol` | string | `eregion` |
| `protocol_version` | int | `1` |
| `worker_id` | string | **mesmo** do hello / CLI |
| `generation` | int | **mesmo** do hello / CLI |
| `pid` | int | `getmypid()` |
| `php_version` | string | `PHP_VERSION` |
| `mithril_version` | string | versão do pacote |

Validação PHP ao receber `hello`:

- `type === hello`
- `protocol === eregion`
- `protocol_version === 1` (versão suportada)
- `worker_id` e `generation` batem com CLI (ou aceitar os do hello se CLI ausente — preferir validar consistência)

Falha de handshake → não entrar no loop → exit `21` (protocol failure).

### 6.3 Request envelope (Go → PHP)

| Campo | Tipo MessagePack | Notas |
|-------|------------------|-------|
| `type` | string | `request` |
| `version` | uint | `1` |
| `id` | string | correlação obrigatória |
| `method` | string | `GET`, `POST`, ... |
| `uri` | string | path + query (`RequestURI`) |
| `path` | string | path only |
| `query` | string | raw query sem `?` |
| `protocol` | string | `HTTP/1.1` |
| `headers` | map[string][]string | headers repetidos preservados |
| `body` | **bin** | bytes crus; sem Base64 |
| `remote_address` | string | IP remoto |
| `host` | string | Host |
| `scheme` | string | `http` / `https` |
| `timeout_ms` | uint | deadline informativo do worker |

### 6.4 Response envelope (PHP → Go)

| Campo | Tipo | Notas |
|-------|------|-------|
| `type` | string | `response` |
| `version` | uint | `1` |
| `id` | string | **igual** ao request `id` |
| `status` | uint | 100–599 |
| `headers` | map[string][]string | preservar `Set-Cookie` múltiplo |
| `body` | **bin** | bytes crus |
| `error` | map\|nil | erro de protocolo opcional (não usar para HTTP 500 de app) |
| `meta` | map | ver §6.5 |

Mismatch de `id` é tratado pelo Go como falha de protocolo (502 + discard). O PHP **nunca** deve alterar o `id`.

### 6.5 Response metadata (`meta`)

| Campo | Tipo | Semântica |
|-------|------|-----------|
| `requests_handled` | uint | contagem após este request |
| `memory_usage` | uint | `memory_get_usage(true)` bytes |
| `memory_peak` | uint | `memory_get_peak_usage(true)` bytes |
| `recycle` | bool | worker pedirá saída após esta resposta |
| `recycle_reason` | string\|omit | `max_requests`, `memory_limit`, etc. |

**Ordem crítica (não negociável):**

```text
receber request
→ beginScope()
→ executar HttpApplication
→ endScope() em finally
→ medir memória
→ avaliar RecyclingPolicy
→ montar response + meta
→ ENVIAR frame de resposta
→ se recycle=true: sair do loop e exit recycled
```

A resposta bem-sucedida **deve ser enviada antes** do processo sair. Caso contrário o cliente perde o último request.

### 6.6 Erro de aplicação vs erro de protocolo

| Situação | Comportamento PHP | Efeito no Eregion |
|----------|-------------------|-------------------|
| Exception tratada → HTTP 500 | Response válido `status=500` | Worker **permanece** saudável |
| MessagePack inválido / frame oversized | Exception tipada; exit `21` | Discard + replace (crash) |
| `id` errado na resposta | Bug — não fazer | Go descarta worker |
| Escopo `endScope()` falha | Não processar próximo request; exit `22` | Replace |
| Pedido de recycle cooperativo | `meta.recycle=true` + exit `10` | Replace **sem** penalidade de crash |

### 6.7 Shutdown remoto (opcional MVP+, recomendado)

Se Eregion enviar mensagem de shutdown/drain no futuro:

```text
type: shutdown
reason: graceful|drain
```

PHP deve:

1. parar de aceitar novos requests no loop;
2. se estiver idle, sair com `0` ou `10` conforme política;
3. se estiver mid-request, terminar o request atual e depois sair.

No Eregion Go atual, recycle/shutdown frequentemente fecha o socket / envia SIGTERM. O worker deve tratar EOF na leitura como encerramento limpo quando já em draining, ou protocol/connection closed caso contrário.

---

## 7. Componentes a implementar

### 7.1 Estrutura de arquivos proposta

```text
mithrilphp/
├── src/Runtime/
│   ├── Worker.php                      # evoluir (resultado estruturado)
│   ├── WorkerResult.php
│   ├── WorkerStopReason.php
│   ├── WorkerMetadata.php
│   ├── WorkerExitCode.php
│   ├── Recycling/
│   │   ├── RecyclingPolicy.php
│   │   ├── RecyclingDecision.php
│   │   ├── WorkerContext.php
│   │   ├── MaxRequestsPolicy.php
│   │   ├── MemoryLimitPolicy.php
│   │   └── CompositeRecyclingPolicy.php
│   └── Eregion/
│       ├── EregionBridge.php
│       ├── WorkerMetadataAwareBridge.php
│       ├── FrameReader.php
│       ├── FrameWriter.php
│       ├── RequestMapper.php
│       ├── ResponseMapper.php
│       ├── Protocol.php
│       ├── Manifest.php
│       ├── Messages/
│       │   ├── HelloMessage.php
│       │   ├── ReadyMessage.php
│       │   ├── RequestEnvelope.php
│       │   ├── ResponseEnvelope.php
│       │   ├── ResponseMetadata.php
│       │   ├── ProtocolError.php
│       │   └── ShutdownMessage.php
│       └── Exceptions/
│           ├── ProtocolException.php
│           ├── InvalidFrameException.php
│           ├── FrameTooLargeException.php
│           ├── HandshakeException.php
│           ├── UnsupportedProtocolVersionException.php
│           ├── UnexpectedMessageException.php
│           └── ConnectionClosedException.php
├── bin/
│   ├── forge
│   └── eregion-worker
└── tests/
    ├── Unit/Runtime/...
    └── Integration/Eregion/...
```

Não vazar MessagePack para o domínio HTTP da aplicação.

### 7.2 `EregionBridge`

Responsabilidades:

- criar/bind/listen UDS no path recebido;
- `chmod` 0600 no socket quando possível;
- `accept` da conexão do Eregion;
- handshake completo;
- loop: ler frame → unpack → validar envelope → mapear Request;
- implementar `RequestBridge` (ou adaptação existente);
- `respond(Response, WorkerMetadata)` via capability (§7.6);
- fechar/remover socket no shutdown.

**Não** chamar `beginScope`/`endScope` — isso é do `Worker`.

### 7.3 Mappers

`RequestMapper`:

- montar `Request` Mithril a partir do envelope;
- preservar headers repetidos;
- body binário;
- query, remote address, host, scheme, protocol HTTP.

`ResponseMapper`:

- status válido 100–599;
- headers com multi-value (`Set-Cookie`);
- body como MessagePack **bin**;
- anexar `meta` de `WorkerMetadata`;
- `id` copiado do request.

### 7.4 Políticas de recycle

```php
interface RecyclingPolicy
{
    public function evaluate(WorkerContext $context): RecyclingDecision;
}
```

Políticas iniciais:

1. `MaxRequestsPolicy` → reason `max_requests`
2. `MemoryLimitPolicy` → reason `memory_limit` (bytes via `memory_get_usage(true)`)
3. `CompositeRecyclingPolicy` → primeira decisão `shouldRecycle=true` vence

`0` em max requests ou memory desativa a política correspondente.

### 7.5 Resultado estruturado do Worker

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

Preferência de API:

1. inspecionar usos públicos de `Worker::run(): int`;
2. preferir `runResult(): WorkerResult` + wrapper compatível, **ou** major bump.

### 7.6 Metadata no bridge (Option A — preferida)

```php
interface WorkerMetadataAwareBridge extends RequestBridge
{
    public function respond(
        Response $response,
        WorkerMetadata $metadata
    ): void;
}
```

O Worker usa capability check; bridges sem metadata continuam funcionando em outros modos.

---

## 8. Forge CLI

### 8.1 `vendor/bin/forge serve`

Responsabilidades:

1. resolver kernel da aplicação;
2. validar contrato `HttpApplication`;
3. validar PHP + `ext-msgpack` (+ sockets);
4. validar/compilar container e routes;
5. validar `eregion.yaml` se presente (ou gerar config mínima);
6. gerar `var/runtime/eregion.json`;
7. localizar binário Eregion;
8. `pcntl_exec` / process replace para:

```bash
eregion serve --config=... --manifest=...
```

9. preservar exit code do Eregion.

Overrides úteis:

```bash
vendor/bin/forge serve --host=0.0.0.0 --port=8080 --workers=4
```

**Não** implementar HTTP server em PHP.

### 8.2 `vendor/bin/forge server:check`

Checks (§23.8 da spec pai), em especial:

- PHP / extensões;
- kernel;
- artifacts;
- `eregion-worker`;
- writability do `socket.directory`;
- binário Eregion e compatibilidade de protocolo;
- validade da config.

### 8.3 `vendor/bin/forge server:install`

Resolver binário Eregion:

1. `EREGION_BINARY`
2. `eregion` no `PATH`
3. `.mithril/bin/eregion` local
4. download pinado com checksum (via `server:install`)

Sem download interativo surpresa em produção.

### 8.4 Outros comandos úteis

```bash
vendor/bin/forge server:status
vendor/bin/forge server:version
vendor/bin/forge optimize
```

`status` pode consultar `GET /_eregion/health` do processo em execução.

---

## 9. Códigos de saída do worker

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

Interpretação no Eregion:

| Código | Significado | Conta como crash? |
|--------|-------------|-------------------|
| `0` | stop normal / shutdown | não |
| `10` | recycle planejado | **não** |
| `20+` | falha | sim (backoff + restart window) |
| sinal / unknown | crash | sim |

Documentar e testar o mapeamento `WorkerStopReason` → `WorkerExitCode`.

---

## 10. Loop recomendado (pseudocódigo)

```php
$bridge->listenAndHandshake($opts);

$handled = 0;
while (true) {
    $envelope = $bridge->receiveRequest(); // ou EOF → break limpo
    $request = $requestMapper->map($envelope);

    $response = null;
    $scopeFailed = false;
    try {
        $worker->beginScope();
        $response = $app->handle($request);
    } catch (Throwable $e) {
        $response = $errorHandler->toResponse($e); // tipicamente 500
    } finally {
        try {
            $worker->endScope();
        } catch (Throwable $e) {
            $scopeFailed = true;
        }
    }

    $handled++;
    $memory = memory_get_usage(true);
    $peak = memory_get_peak_usage(true);

    $decision = $policies->evaluate(new WorkerContext(
        requestsHandled: $handled,
        memoryUsage: $memory,
        memoryPeak: $peak,
        lastError: null,
    ));

    $meta = new WorkerMetadata(
        requestsHandled: $handled,
        memoryUsage: $memory,
        memoryPeak: $peak,
        recycle: $decision->shouldRecycle || $scopeFailed,
        recycleReason: $scopeFailed ? 'scope_cleanup_failure' : $decision->reason,
    );

    $bridge->respond($response, $meta); // SEMPRE enviar se possível

    if ($scopeFailed) {
        exit(WorkerExitCode::ScopeCleanupFailure->value);
    }
    if ($meta->recycle) {
        exit(WorkerExitCode::Recycled->value);
    }
}
```

---

## 11. Critérios de aceite (lado PHP)

A integração MithrilPHP está pronta quando:

- [ ] `bin/eregion-worker` sobe com as flags do Eregion
- [ ] UDS é criado pelo PHP; handshake `hello`/`ready` completa
- [ ] frames length-prefixed + MessagePack round-trip corretos
- [ ] bodies binários sem Base64
- [ ] headers repetidos (`Set-Cookie`) preservados
- [ ] kernel boots **uma vez** por processo
- [ ] scoped services são recriados por request; sem leak entre requests
- [ ] `endScope()` roda mesmo após exception da app
- [ ] HTTP 500 de app **não** invalida o worker
- [ ] `meta.recycle` + exit `10` após max requests / memory
- [ ] última resposta é enviada **antes** do exit de recycle
- [ ] falha de protocolo / frame → exit `21`
- [ ] falha de `endScope` → exit `22` sem aceitar próximo request
- [ ] Forge gera manifest e faz exec do Eregion
- [ ] `forge server:check` valida ext-msgpack e binário
- [ ] testes unitários e de lifecycle passam (§12)

---

## 12. Testes obrigatórios na lib PHP

### 12.1 Unitários

- `MaxRequestsPolicy`, `MemoryLimitPolicy`, composite ordering
- `RecyclingDecision`, `WorkerResult`, exit-code mapping
- `FrameReader` / `FrameWriter` (partial, oversized, empty)
- exceções tipadas
- `RequestMapper` / `ResponseMapper`
- parsing do manifest
- parsing das flags CLI

### 12.2 Integração de lifecycle

- boot do kernel uma vez por worker
- singleton persiste entre requests no mesmo processo
- scoped service isolado por request
- sem leak de estado do request anterior
- `endScope()` após exception
- exit após max-request com resposta final enviada
- exit por memory com reason
- metadata contém reason
- 500 de app não força recycle
- EOF/shutdown encerra limpo

### 12.3 Compatibilidade com Eregion

Rodar contra o binário deste repo (ou fixtures Go) cobrindo:

- GET / JSON / binary body
- headers repetidos
- recycle planejado reconhecido pelo Go sem crash penalty
- mismatch de protocolo detectado

---

## 13. Fora de escopo (PHP v1)

- Server HTTP embutido em PHP
- Codec JSON opcional
- Streaming de frames
- Named pipes Windows
- Autoscaling / multi-app no mesmo worker
- Polling RSS via `/proc` (medição cooperativa basta)
- Dashboard / plugin system

---

## 14. Checklist de implementação sugerida

### Fase A — Protocol foundation
1. Messages + Exceptions  
2. FrameReader/Writer + testes  
3. Handshake em isolation  

### Fase B — Bridge + mappers
4. RequestMapper / ResponseMapper  
5. EregionBridge (listen UDS + loop)  
6. WorkerMetadataAwareBridge  

### Fase C — Recycle + Worker
7. Policies + WorkerContext/Decision  
8. Evoluir Worker loop/result/exit codes  
9. `bin/eregion-worker`  

### Fase D — Forge
10. Manifest writer  
11. `forge serve` (exec Eregion)  
12. `forge server:check` / `server:install`  

### Fase E — Hardening
13. Testes lifecycle  
14. Testes contra Eregion real  
15. Documentação do pacote Mithril  

---

## 15. Definição final

A lib PHP é o **lado aplicativo do contrato EREGION/1**:

> Eregion mantém a forja ligada.  
> MithrilPHP mantém a aplicação aquecida, isolada por request, e decide cooperativamente quando forjar um processo novo.

**Build with Mithril. Run in Eregion.**
