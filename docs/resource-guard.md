# Resource Guard (ECO-09)

O Resource Guard é um controlador global de admissão. As estratégias `fixed`, `backlog` e `resources` continuam calculando a demanda. O guard decide quantos desses workers cabem no orçamento e por quê.

O protocolo permanece `eregion/1`. O intervalo padrão de coleta é 1 s. Os limiares de memória e CPU não foram alterados para acomodar o benchmark.

## Modos

| Modo | Efeito | Padrão |
| --- | --- | --- |
| `observe` | Publica o alvo seguro e `would_limit`. A contagem de workers segue o caminho anterior. | sim, quando a chave falta |
| `enforce` | A contagem admitida é o alvo seguro. Spawn, restart e boot passam pelo ledger. | opt-in |
| `off` | Não amostra e não reserva. O reconciler antigo permanece. | escape hatch |

## `hard_limit_mb` é orçamento de admissão

`resource_guard.memory.hard_limit_mb` não é um limite físico de memória. O processo não grava `memory.max`, `memory.limit_in_bytes`, `RLIMIT_AS` nem envia `SIGKILL` para caber nesse número. Um worker já admitido pode crescer além do orçamento; o kernel continua sendo quem mata por OOM, se isso acontecer.

A regra de combinação é:

- com limite e consumo de cgroup conhecidos, o orçamento efetivo é o menor entre o cgroup e `hard_limit_mb`;
- sem limite de cgroup, e com o RSS da árvore de processos gerenciados conhecido, o orçamento vale para essa árvore;
- sem as duas pontas do mesmo escopo, `enforce` não inventa RAM do host e falha o boot com `resource_limit_unknown`.

`0` significa “não instalar teto local”. Não significa memória infinita.

## Ledger

O custo de um worker novo é `max(memory_mb, RSS p95 conhecido, worker_fallback_mb)`. `memory_mb: 0` não é de graça. Workers que já estão no `memory.current` não entram de novo nessa conta.

1. **Admissão inicial.** `Admit` reserva o custo projetado antes do `Start`. Enquanto a reserva está pendente, ela reduz o headroom. Uma admissão que estouraria `limite − corrente − reserva − pendente` é recusada.
2. **Reconciliação com RSS real.** Na primeira amostra com instante estritamente posterior ao commit, a reserva sai do pendente. O RSS medido passa a ser o consumo. Se o processo ocupa menos que o custo projetado, o headroom reabre e outra admissão pode caber.
3. **Crescimento posterior.** Se o RSS dos workers já admitidos sobe e o headroom fica menor que o custo de um worker novo, `Admit` recusa o próximo spawn. A reserva antiga não é somada de novo. O plano mantém `admitted` igual ao número que já está rodando: não há drenagem nem sinal novo por causa desse crescimento.

O teste `TestECO09LedgerBudgetFollowsReservationThenRSS` cobre os três passos. T18–T21 cobrem concorrência, rollback único, saída única e substituição 1:1. T23 e T24 cobrem o drain já existente, sem `SIGKILL` preventivo.

## Gate de performance

Artefatos oficiais, sem amostra removida:

- `/home/thales/wslbench/mithrilphp/bench/results/resource-guard-ab.json`
- `/home/thales/wslbench/mithrilphp/bench/results/resource-guard-ab-method.json`
- `/home/thales/wslbench/mithrilphp/bench/results/resource-guard-saturation.json`
- `/home/thales/wslbench/mithrilphp/bench/results/profiles/`

Vinte pares, janela de 60 s, ordem balanceada `off` / `observe` / `enforce`, processo do `eregion serve` separado do gerador. Veredito oficial: **PASS**.

| Estimador | Queda de vazão | Aumento de p99 |
| --- | --- | --- |
| Mediana | 0,066% | 0,138% |
| Média | −0,476% | −0,683% |
| IC 95%, t = 2,093, 19 gl | −1,360% a 0,408% | −1,970% a 0,603% |

Limites do gate: 2% e 5%. Zero respostas 5xx nas rodadas sem saturação. Saturação em 384 MiB: 1 HTTP e 1 `work` admitidos, 3 HTTP limitados, 78608 respostas 503, zero 502 e 504.

O par 20 permanece na série. Nele o `off` rodou por último e a vazão da máquina já tinha caído: queda de −7,95% e p99 de −11,21% (observe mais rápido). A média é puxada por esse ponto; a mediana não. A conta sem esse par, com t de 18 graus de liberdade, está em [`eco-09/gate-sensitivity.json`](eco-09/gate-sensitivity.json). Ela não é o veredito.

Os perfis de CPU, alocação, goroutine e mutex foram colhidos fora das 20 rodadas. `ProcessTreeRSS` não apareceu no CPU. Não houve otimização do guard.

## Matriz ECO09-T01–T38

| ID | Teste |
| --- | --- |
| T01 | `TestECO09T01ClampDesiredDoesNotReapplyMin` |
| T02 | `TestECO09T02MinFitsWithoutWarning` |
| T03 | `TestECO09T03StartAllSpawnsNothingWhenMinimumsDoNotFit` |
| T04 | `TestECO09T04ObserveKeepsLegacyTarget` |
| T05 | `TestECO09T05OffDoesNotPublishABlock` |
| T06 | `TestECO09T06SharedBudgetAndFairShare` |
| T07 | `TestECO09T07PermutedSpecsProduceTheSamePlan` |
| T08 | `TestECO09T08NoStarvation` |
| T09 | `TestECO09T09ConsumerMinZero` |
| T10 | `TestECO09T10ResourcesIgnoreGOMAXPROCS` |
| T11 | `TestECO09T11CPUQuotaChangesWithoutRestart` |
| T12 | `TestECO09T12CgroupV2Memory` |
| T13 | `TestECO09T13CgroupV1Memory` |
| T14 | `TestECO09T14NestedParentIsTighter`, `TestECO09T14UnreadableParentDoesNotClaimLeafLimit` |
| T15 | `TestECO09T15PermissionIsNotZeroUsage` |
| T16 | `TestECO09T16StaleSampleBlocksAdmit` |
| T17 | `TestECO09T17InvalidSampleBlocksThenFreshSampleAllowsGrowth` |
| T18 | `TestECO09T18ConcurrentReservationsDoNotOversubscribe` |
| T19 | `TestECO09T19ReleaseOnce` |
| T20 | `TestECO09T20ExitReleaseOnce` |
| T21 | `TestECO09T21ReplacementDoesNotDoubleCharge` |
| T22 | `TestECO09T22BootSlotAdmitsBeforeStart` |
| T23 | `TestECO09T23AdmittedScaleDownUsesExistingDrain` |
| T24 | `TestECO09T24BusyHTTPScaleDownWaitsForRelease` |
| T25 | `TestECO09T25CPUSoftCapDoesNotShrink` |
| T26 | `TestECO09T26CPUThrottlingIsCritical` |
| T27 | `TestECO09T27MemoryCriticalBlocksGrowth` |
| T28 | `TestECO09T28WarningDedupeAndHysteresis` |
| T29 | `TestECO09T29InsufficientHistoryIsNotANumber` |
| T30 | `TestECO09T30LinearGrowthIsAuditable` |
| T31 | `TestECO09T31SourceChangeResetsHistory` |
| T32 | `TestECO09T32GuardMetricsUseClosedLabels` |
| T33 | `TestECO09T33ScrapeDoesNotInventUnknownMemory` |
| T34 | `TestECO09T34GuardMetricsRespectLoopback` |
| T35 | `TestECO09T35InvalidGuardPaths` |
| T36 | `TestECO09T36MixedStrategiesKeepTheirContracts` |
| T37 | `TestECO09T37PreflightUnknownLimit`, `TestECO09T37HostCeilingRequiresManagedConsumption` |
| T38 | `TestECO09T38LocalCeilingUsesTheSmallerCgroupLimit` |

T23 e T24 usam o drain existente. O teste de SIGTERM do scale-down de consumer é específico de Linux e faz skip no Windows.

## Definition of Done

| Critério | Evidência |
| --- | --- |
| Baseline e HEAD registrados | implementação sobre `c4db68caeb992f93879e9165df302b169ed2474b`; gate medido no código atual |
| Clamp de mínimo corrigido | T01 |
| Config com padrão compatível | `observe`; T35; `eregion check` imprime o modo e não amostra |
| Sampler fora do handler HTTP | `Collector.Run`; perfil de CPU sem o sampler no caminho de `/item` |
| Orçamento sem dupla contagem | T06, T18–T21 e `TestECO09LedgerBudgetFollowsReservationThenRSS` |
| Gate em todo spawn | `bootSlot` admite antes de iniciar; T03, T22 |
| Sem SIGKILL preventivo | T23, T24; crescimento de RSS não reduz `admitted` abaixo de `running` |
| Warnings com dedupe | T28 |
| Métricas com labels fechados | T32, T33, T34 |
| Preditor conservador | T29–T31; previsão não altera admissão |
| T01–T38 | tabela acima; execução na seção de testes do fechamento |
| A/B reprodutível | PASS oficial de 20×60 s, série completa |
| `EREGION/1` intacto | `ProtocolVersion = 1`; nenhuma mudança de frame |

## Riscos residuais

- O orçamento não contém o RSS depois da admissão. Em `enforce`, o processo pode ultrapassar `hard_limit_mb` e continuar até o kernel agir.
- No WSL usado no gate, o cgroup de memória é ilimitado. O teto efetivo foi a árvore de processos, não um `memory.max`.
- A previsão não participa da admissão. Um crescimento rápido entre amostras de 1 s só é visto na amostra seguinte.
- O par 20 mostra deriva da máquina ao longo da hora. Ele permanece na série; a incerteza oficial já o inclui.
- `EREGION_PPROF` só escuta em binário compilado com `-tags pprof`.
