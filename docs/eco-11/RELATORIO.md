# ECO-11 — verificação final

Data da medição oficial: 2026-11-24 00:12–02:28 UTC. Host de desenvolvimento: Windows 10. A medição correu em WSL2 ext4 (`/home/thales/eregion-eco11`), fora de `/mnt/c`. Nenhum commit, push, tag ou publicação foi feito. EREGION/1, ECO-09 e ECO-10 não foram alterados. Os limites permanecem 2% de perda mediana de throughput e 5% de alta mediana de p99, com o limite superior do IC 95% pareado dentro do mesmo teto.

DONE LOCAL está declarado. O benchmark permanece PASS e não foi reexecutado. T58 passou numa VPS com cgroup v2 e quota real. T54 continua BLOCKED porque nada foi publicado.

## 1. Matriz T01–T64

Comando Go comum, executado nesta árvore depois dos testes novos: `go test -count=1 -timeout 300s ./internal/...` e o mesmo para `./tests/integration/...`, em Windows (Go 1.23.4, PHP 8.5.10) e em Linux ext4 (Go 1.27.4, PHP 8.5.9). `go test -race -count=1 -timeout 600s ./internal/...` e `go vet ./...` passaram nos dois ambientes em que cada um se aplica; o race detector rodou só em Linux, com gcc. O gate de performance não entra nesses comandos: ele só corre com `EREGION_ECO11_BENCH=1`.

| ID | Requisito comprovado | Evidência | Plataforma | Comando | Resultado |
| --- | --- | --- | --- | --- | --- |
| T01 | legado sem blocos ECO-11 conserva fixed, backlog e resources | `TestBuildLegacyOnly`, `TestLegacyResolvesHTTPWorkload`, `TestFixedStrategy`, `TestECO09T36MixedStrategiesKeepTheirContracts` | Windows e Linux | `go test -count=1 ./internal/workload/ ./internal/scaling/ ./internal/config/` | PASS |
| T02 | ausente, zero, campo desconhecido e duplicata | `TestECO11T02AbsentZeroAndUnknownFields` | Windows e Linux | `go test -count=1 -run TestECO11T02 ./internal/config/` | PASS |
| T03 | reset de contador sem underflow | `TestECO11T03CounterResetDoesNotUnderflow` | Windows e Linux | `go test -count=1 -run TestECO11T03 ./internal/scaling/signals/` | PASS |
| T04 | epoch parcial ou repetido não confirma | `TestECO11T04PartialAndRepeatedEpochDoesNotConfirm` | Windows e Linux | `go test -count=1 -run TestECO11T04 ./internal/scaling/signals/` | PASS |
| T05 | zero medido é distinto de desconhecido | `TestECO11T05MeasuredZeroIsNotUnknown` | Windows e Linux | `go test -count=1 -run TestECO11T05 ./internal/scaling/signals/` | PASS |
| T06 | rota HTTP medida e prefixo operacional fora da série | `TestRequestPathsObserveDurationOnce` subteste `operations prefix` | Windows e Linux | `go test -count=1 -run TestRequestPathsObserveDurationOnce ./internal/dispatcher/` | PASS |
| T07 | p95 com amostra pequena ou faixa ambígua fica inválido | `TestECO11T07SmallHistogramIsNotAP95` | Windows e Linux | `go test -count=1 -run TestECO11T07 ./internal/scaling/signals/` | PASS |
| T08 | reutilização de PID invalida CPU | `TestECO11T08PIDReuseInvalidatesCPU` | Windows e Linux | `go test -count=1 -run TestECO11T08 ./internal/scaling/signals/` | PASS |
| T09 | pools não compartilham CPU | `TestECO11T09PoolsDoNotShareCPU` | Windows e Linux | `go test -count=1 -run TestECO11T09 ./internal/scaling/signals/` | PASS |
| T10 | troca de source ou quota de cgroup zera a janela | `TestECO11T10QuotaOrSourceChangeResetsWindow` | Windows e Linux | `go test -count=1 -run TestECO11T10 ./internal/scaling/signals/` | PASS |
| T11 | CPU de workload ausente recusa `cpu_utilization` | `TestECO11T11CPURejectedWithoutWorkerCPU` | Windows e Linux | `go test -count=1 -run TestECO11T11 ./internal/workload/` | PASS |
| T12 | Forget apaga histórico | `TestECO11T12ForgetDropsHistory` | Windows e Linux | `go test -count=1 -run TestECO11T12 ./internal/scaling/signals/` | PASS |
| T13 | CPU 0,90 sobre alvo 0,65 pede 6 e sobe um passo | `TestECO11T13CPUHighRequestsSixThenSteps` | Windows e Linux | `go test -count=1 -run TestECO11T13 ./internal/scaling/` | PASS |
| T14 | CPU baixo desce um passo | `TestECO11T14CPULowStepsDown` | Windows e Linux | `go test -count=1 -run TestECO11T14 ./internal/scaling/` | PASS |
| T15 | veto de CPU global não encolhe | `TestECO11T15GlobalCPUVetoDoesNotShrink` | Windows e Linux | `go test -count=1 -run TestECO11T15 ./internal/scaling/` | PASS |
| T16 | consumer sem backlog fresco não desce por CPU baixo | `TestECO11T16ConsumerCPUWithoutBacklogDoesNotScaleDown` | Windows e Linux | `go test -count=1 -run TestECO11T16 ./internal/scaling/` | PASS |
| T17 | `cpu_utilization` com `workers.min=0` morre em `config.Load` | `TestECO11T17CPUMinZeroRejected`, `TestResolveWorkloadsRejectsCPUUtilizationMinZero` | Windows e Linux | `go test -count=1 -run 'TestECO11T17\|TestResolveWorkloadsRejectsCPUUtilizationMinZero' ./internal/config/` | PASS |
| T18 | latência alta com fila e busy pede +1 | `TestECO11T18LatencyPressureStepsUp` | Windows e Linux | `go test -count=1 -run TestECO11T18 ./internal/scaling/` | PASS |
| T19 | latência alta sem fila segura o nível | `TestECO11T19LatencyWithoutQueueHolds` | Windows e Linux | `go test -count=1 -run TestECO11T19 ./internal/scaling/` | PASS |
| T20 | latência baixa com fila desconhecida não desce | `TestECO11T20LatencyLowWithUnknownQueueHolds` | Windows e Linux | `go test -count=1 -run TestECO11T20 ./internal/scaling/` | PASS |
| T21 | throughput usa offered, inclusive rejeições | `TestECO11T21ThroughputUsesOffered` | Windows e Linux | `go test -count=1 -run TestECO11T21 ./internal/scaling/` | PASS |
| T22 | alvo de throughput ausente é erro de config | `TestECO11T22ThroughputTargetRequired` | Windows e Linux | `go test -count=1 -run TestECO11T22 ./internal/config/` | PASS |
| T23 | cooldown de um workload não segura o outro | `TestECO11T23CooldownDoesNotCrossWorkloads` | Windows e Linux | `go test -count=1 -run TestECO11T23 ./internal/scaling/` | PASS |
| T24 | rejeição local não veta expansão | `TestECO11T24LocalRejectsDoNotVeto` | Windows e Linux | `go test -count=1 -run TestECO11T24 ./internal/scaling/` | PASS |
| T25 | hybrid sobe pelo máximo e não pela soma | `TestECO11T25HybridTakesMaxUpNotSum` | Windows e Linux | `go test -count=1 -run TestECO11T25 ./internal/scaling/` | PASS |
| T26 | hybrid sobe por fila com CPU e p95 ausentes | `TestECO11T26HybridQueueUpWhenCPUAndP95Missing` | Windows e Linux | `go test -count=1 -run TestECO11T26 ./internal/scaling/` | PASS |
| T27 | HTTP ocioso desce sem p95 fabricado | `TestECO11T27IdleHTTPStepsDownWithoutFakeP95` | Windows e Linux | `go test -count=1 -run TestECO11T27 ./internal/scaling/` | PASS |
| T28 | hybrid consumer lê o backlog uma vez | `TestECO11T28HybridConsumerBacklog` | Windows e Linux | `go test -count=1 -run TestECO11T28 ./internal/scaling/` | PASS |
| T29 | consumer min 0 acorda com backlog e trata zero medido | `TestECO11T29MinZeroWakesOnBacklog` | Windows e Linux | `go test -count=1 -run TestECO11T29 ./internal/scaling/` | PASS |
| T30 | backlog stale segura o nível atual | `TestECO11T30StaleBacklogHolds` | Windows e Linux | `go test -count=1 -run TestECO11T30 ./internal/scaling/` | PASS |
| T31 | hybrid consumer sem `http_json` é rejeitado no Load | `TestECO11T31HybridConsumerWithoutSourceRejected` | Windows e Linux | `go test -count=1 -run TestECO11T31 ./internal/config/` | PASS |
| T32 | permutação de workloads produz o mesmo plano | `TestECO09T07PermutedSpecsProduceTheSamePlan` | Windows e Linux | `go test -count=1 -run TestECO09T07 ./internal/scaling/` | PASS |
| T33 | enforce não religa o mínimo e não arranca se o mínimo não cabe | `TestECO09T01ClampDesiredDoesNotReapplyMin`, `TestECO09T03StartAllSpawnsNothingWhenMinimumsDoNotFit` | Windows e Linux | `go test -count=1 -run 'TestECO09T01\|TestECO09T03' ./internal/scaling/ ./internal/worker/` | PASS |
| T34 | modo off recusa estratégia nova | `TestECO11T34OffRejectsNewStrategy` | Windows e Linux | `go test -count=1 -run TestECO11T34 ./internal/config/` | PASS |
| T35 | janela de schedule é semiaberta em UTC | `TestECO11T35ScheduleEdgesAreHalfOpen` | Windows e Linux | `go test -count=1 -run TestECO11T35 ./internal/scaling/` | PASS |
| T36 | overlap e timezone fora de UTC são rejeitados | `TestECO11T36OverlapAndTimezoneRejected` | Windows e Linux | `go test -count=1 -run TestECO11T36 ./internal/config/` | PASS |
| T37 | salto de relógio escolhe o alvo do instante novo | `TestECO11T37ScheduleClockJumpDoesNotReplay` | Windows e Linux | `go test -count=1 -run TestECO11T37 ./internal/scaling/` | PASS |
| T38 | schedule sobe no máximo um passo e respeita cooldown e veto | `TestECO11T38ScheduleStepAndVeto` | Windows e Linux | `go test -count=1 -run TestECO11T38 ./internal/scaling/` | PASS |
| T39 | probe com ganho estável retém N+1 | `TestECO11T39AdaptiveAcceptsGain` | Windows e Linux | `go test -count=1 -run TestECO11T39 ./internal/scaling/` | PASS |
| T40 | ganho fraco faz rollback | `TestECO11T40AdaptiveRollsBackWeakGain` | Windows e Linux | `go test -count=1 -run TestECO11T40 ./internal/scaling/` | PASS |
| T41 | mudança de offered confunde o probe | `TestECO11T41OfferedShiftIsConfounded` | Windows e Linux | `go test -count=1 -run TestECO11T41 ./internal/scaling/` | PASS |
| T42 | probe bloqueado pelo guard não vira sucesso | `TestECO11T42BlockedProbeDoesNotSucceed` | Windows e Linux | `go test -count=1 -run TestECO11T42 ./internal/scaling/` | PASS |
| T43 | timeout de probe volta à origem e não grava ganho | `TestECO11T43ProbeTimeoutRollsBackWithoutGain` | Windows e Linux | `go test -count=1 -run TestECO11T43 ./internal/scaling/` | PASS |
| T44 | adaptive exige enforce e HTTP | `TestECO11T44AdaptiveRequiresEnforceAndHTTP` | Windows e Linux | `go test -count=1 -run TestECO11T44 ./internal/config/` | PASS |
| T45 | um probe ativo por vez | `TestECO11T45OneProbeAtATime` | Windows e Linux | `go test -count=1 -run TestECO11T45 ./internal/scaling/` | PASS |
| T46 | settle, cooldown e Forget descartam estado antigo | T12, T23, T39 e T43 | Windows e Linux | os comandos desses quatro testes | PASS |
| T47 | rótulos de métrica e razões ficam num conjunto fechado | `TestWorkloadMetricLabels`, `TestECO09T32GuardMetricsUseClosedLabels`, `TestExitReasonIsClosedEnum` | Windows e Linux | `go test -count=1 ./internal/telemetry/` | PASS |
| T48 | scrape não amostra e métricas ficam no loopback | `TestCollectorDoesNotSampleOnCurrent`, `TestECO09T34GuardMetricsRespectLoopback` | Windows e Linux | `go test -count=1 ./internal/resources/ ./internal/server/` | PASS |
| T49 | CLI recusa combinação inválida antes de mutar | `WorkloadOptionPolicyTest`, `InitSelectionTest` | Windows, PHP 8.5.10 | `vendor/bin/phpunit tests/Unit` em `durin-presets` e `durins-forge` | PASS |
| T50 | política YAML inválida é rejeitada | suíte `durin-presets` tests/Unit, 49 testes | Windows | `vendor/bin/phpunit --do-not-cache-result tests/Unit` | PASS |
| T51 | segundo init e isolamento de contexto | `InitSelectionTest`, `ContextIsolationTest` | Windows | `vendor/bin/phpunit --do-not-cache-result tests/Unit` em `durins-forge` | PASS |
| T52 | `--skip-runtime-install` falha antes de persistir instalação | `InitSelectionTest` | Windows | mesmo comando de T51 | PASS |
| T53 | catálogo e README gerado | `GeneratedPresetContractTest` e `PackageContractTest` passaram carregando o checkout irmão, não o dist v0.3.6 | Windows, PHP 8.5.10 | `vendor/bin/phpunit --do-not-cache-result tests/Unit` em `durins-forge` | PASS |
| T54 | release publicada | nenhuma versão foi publicada | — | — | BLOCKED |
| T55 | E2E HTTP sobe, aguanta burst e desce | `docs/eco-11/results/e2e/t55.json`, PID 1832 | Linux ext4 | `EREGION_ECO11_E2E=1 go test -count=1 -timeout 15m -run TestECO11FunctionalE2E ./tests/integration/` | PASS |
| T56 | E2E consumer com provider real, wake-up e drain | `docs/eco-11/results/e2e/t56.json`, PID 1874 | Linux ext4 | o mesmo comando de T55 | PASS |
| T57 | E2E latência externa sem fila | `docs/eco-11/results/e2e/t57.json`, PID 2017, 224 respostas no upstream | Linux ext4 | o mesmo harness, subteste T57 | PASS |
| T58 | E2E CPU-bound com throttling | `docs/eco-11/results/e2e/t58.json`, PID 3022687, quota `70000 100000` | VPS cgroup v2 | `EREGION_ECO11_E2E=1 go test -count=1 -timeout 8m -run TestECO11FunctionalE2E/T58 ./tests/integration/` | PASS |
| T59 | E2E pressão de memória até o ledger | `docs/eco-11/results/e2e/t59.json`, PID 1940, uma admissão negada por headroom | Linux ext4 | o mesmo harness, subteste T59 | PASS |
| T60 | E2E probe aceito e rejeitado com worker real | `docs/eco-11/results/e2e/t60.json`, ganho 0,173 e rollback até 1 worker | Linux ext4 | `EREGION_ECO11_E2E=1 go test -count=1 -timeout 15m -run TestECO11FunctionalE2E/T60 ./tests/integration/` | PASS |
| T61 | race, scrape, reconcile e shutdown | `go test -race ./internal/...` e `TestWGShutdown`, `TestE08ShutdownDropsPendingRestart` | Linux para o race; Windows e Linux para o shutdown | `go test -race -count=1 -timeout 600s ./internal/...` | PASS |
| T62 | benchmark de 20 pares | `TestECO11PerformanceGate` | Linux ext4 | `EREGION_ECO11_BENCH=1 go test -count=1 -timeout 6h -run TestECO11PerformanceGate ./tests/integration/` | PASS |
| T63 | churn longo de um supervisor sem oscilar além do passo | `docs/eco-11/results/eco11-churn.json`, PID 1658 nos quatro ciclos | Linux ext4 | `EREGION_ECO11_E2E=1 go test -count=1 -timeout 15m -run TestECO11FunctionalE2E/T63 ./tests/integration/` | PASS |
| T64 | suítes ECO-08, ECO-09 e protocolo EREGION/1 | `./internal/...` e `./tests/integration/...` nos dois SO | Windows e Linux | os comandos da seção 4 | PASS |

T53 passou sem editar o template e sem relaxar o teste. O autoload do dist v0.3.6 (`36f79ab`) ainda gravava `vendor/bin/durin doctor`. O checkout irmão já grava `durins-forge doctor`, `durins-forge run` e `vendor/bin/durins-forge run`. O `composer.json` do Forge não declara `repositories`. Nesta máquina, `vendor/ereborcodeforge/durin-presets` é a junção para esse checkout. A suíte de 2026-11-24 15:34 UTC ficou em 205 testes, 1010 asserções e 3 depreciações já conhecidas. Isso não é release no Packagist. O installer, no mesmo horário, passou 76 testes, com 2 skips. `durin-presets` passou 49 testes e 417 asserções.

## 2. Benchmark oficial

Artefato: `docs/eco-11/results/eco11-performance.json`. SHA-256 `d5a74b13c20e70a218b6f125164037a580de901b5a13857def5c1ff1be020996`. O arquivo de progresso é a última fotografia da comparação em curso e não é o veredito.

Método, gravado em `eco11-performance-method.json`: 20 pares, ordem AB/BA em 10 e 10, aquecimento de 5 s depois da prontidão, janela de 60 s. O gerador de carga é o processo do teste. `eregion serve` é outro grupo de processos. A aplicação é MithrilPHP `bench/http/App.php`. O consumer usa `backlog` com `http_json` devolvendo `{"backlog":0}` e min=max=1. O HTTP fica em min=max=2, com 4 clientes. Nenhuma amostra foi removida. Student t com 19 graus de liberdade, crítico 2,093. Goroutines do supervisor não são exportadas; o artefato guarda threads de `/proc`.

Ambiente da medição: Linux DESKTOP-CFAIQ33, WSL2 5.15.146, linux/amd64, 28 CPUs, MemTotal 20398892 kB, Go 1.27.4, PHP 8.5.9. Baseline `7d6463764aeb965e8dfe4644237139f28dec9ef6`, SHA-256 `984cb98e07b1fcea028b6b380c0cb674e593c5d3f8c7f534852e8014e867320e`, produzido com `git archive` desse commit. Candidato da árvore de trabalho, SHA-256 `5722cc8c6118b37330665e1c588afbfb7d18a45383a5bead2b93e42e1fa5d109`. Duração do teste: 8137,6 s. `BENCH_EXIT:0`.

A perda de throughput é `(rps_base - rps_candidato) / rps_base`. A alta de p99 é `(p99_candidato - p99_base) / p99_base`. Valor negativo significa que o candidato ficou à frente naquela amostra.

| Comparação | Veredito | Mediana RPS | IC 95% RPS | Mediana p99 | IC 95% p99 | 5xx | Drift | SIGKILL |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| legado, observe, binário ECO-09 contra a árvore atual, fixed + backlog | PASS | −0,156% | [−1,099, 0,394] | −0,607% | [−3,467, 0,531] | 0 | não | não |
| candidato fixed contra scheduled com `default_workers=2`, enforce | PASS | −0,267% | [−1,018, −0,030] | −0,509% | [−1,847, 0,014] | 0 | não | não |
| candidato fixed contra `cpu_utilization` min=max, enforce | PASS | −0,333% | [−0,472, 0,524] | −0,278% | [−0,807, 0,840] | 0 | não | não |

Veredito agregado: PASS. Em cada comparação há 40 execuções, workers HTTP solicitados 2 e admitidos 2, consumer em 1, janela de 60 s e zero respostas fora de 200. O par 14 do legado e o par 16 de CPU ficam fora da banda de uma amostra isolada. Os dois permaneceram na série. A mediana e o teto do intervalo continuam dentro de 2% e 5%. `TestECO11GateRefusesIncomplete` recusa PASS quando a série tem menos de 20 pares, janela curta, erro de transporte, SIGKILL, deriva de workers ou intervalo inconclusivo.

## 3. Segurança e concorrência

`go test -race -count=1 -timeout 600s ./internal/...` em Linux ext4 terminou com `RACE_EXIT:0` antes do ajuste do sampler de cgroup e da amostra única por época do probe. `go vet ./...` passou em Windows e em Linux nessa mesma passagem. Os dois não foram repetidos depois desses ajustes. A repetição sem race está na seção 4.

Fixed, resources e backlog continuam no planner antigo: `TestECO09T36MixedStrategiesKeepTheirContracts` e `TestFixedStrategy`. O isolamento entre workloads está em `TestECO11T09PoolsDoNotShareCPU` e `TestPoolManagerIsolation`. Amostra ausente, inválida ou velha fica de fora da decisão em T04, T05, T10 e T30, e o ledger ECO-09 bloqueia admissão com amostra stale em `TestECO09T16StaleSampleBlocksAdmit`.

O passo e o cooldown impedem salto de vários workers: T13, T23 e T38. Nas três comparações com `resource_guard: enforce` ou `observe`, a admissão final do HTTP foi 2, igual ao pedido e ao número em execução. O encerramento de cada um dos 120 supervisores voltou sem SIGKILL. Depois do gate, não restou processo `eregion serve`. O drain gracioso de scale-down segue coberto por `TestECO09T23AdmittedScaleDownUsesExistingDrain` e `TestECO09T24BusyHTTPScaleDownWaitsForRelease`.

## 4. Regressões Windows e Linux

Repetidas em 2026-11-24 15:34–15:37 UTC, depois do sampler de cgroup e da amostra única por época, sem `EREGION_ECO11_E2E` e sem `EREGION_ECO11_BENCH`. O gate oficial fez skip. Skip não é o PASS da seção 2.

Windows, Go 1.23.4:

- `go test -count=1 -timeout 300s ./internal/...` passou. `worker` levou 43,9 s.
- `go test -count=1 -timeout 300s ./tests/integration/...` passou em 28,7 s.

Linux ext4, `/home/thales/eco11-close/eregion`, Go 1.27.4:

- `go test -count=1 -timeout 300s ./internal/...` passou. `worker` levou 28,0 s.
- `go test -count=1 -timeout 300s ./tests/integration/...` passou em 72,9 s.

PHP no Windows, PHP 8.5.10, 15:34 UTC: `durin-presets` 49 testes e 417 asserções; `durin-installer` 76 testes, 237 asserções e 2 skips; `durins-forge` 205 testes, 1010 asserções e 3 depreciações.

## 5. Evidência E2E e o que ficou aberto

O harness é `tests/integration/eco11_e2e_test.go`. Ele só corre com `EREGION_ECO11_E2E=1`, recusa `/mnt/c` e confere o SHA-256 do artefato oficial antes e depois. A cópia usada foi `/home/thales/eco11-close/eregion`, com Mithril em `/home/thales/eco11-close/mithrilphp`. Host DESKTOP-CFAIQ33, WSL2 5.15.146, 28 CPUs, Go 1.27.4, PHP 8.5.9. Os JSON abaixo estão em `docs/eco-11/results/` e não alteram `eco11-performance.json`. O E2E correu depois de dois ajustes que o binário do benchmark não contém: o sampler acha o cgroup2 em `/sys/fs/cgroup/unified` quando não há quota, e o probe adaptativo usa a amostra do guard, uma época fechada por vez. Os limiares 0,95 e 0,20, a janela mínima de 10 s e o ledger não mudaram.

T55, PID 1832. Na carga baixa, requested, admitted e running ficaram em 1, com `scale_up` 0. No burst, offered chegou a 82,2 rps, o p95 da janela a 0,0025 s e o p95 do cliente a 0,0023 s, com 845 respostas ok e 0 falhas. A demanda publicada foi 11; admitted e running subiram de 1 para 2, com um evento de subida. No ocioso, running voltou a 1, com um evento de descida. O PID não mudou.

T56, PID 1874. No ocioso, admitted e running estavam em 0. Com o provider `http_json` em profundidade, admitted e running foram a 2, que é o máximo do cenário, e o sink passou de 0 para 4 bytes. Com profundidade 0, admitted e running voltaram a 0, com um `scale_down`.

T57, PID 2017, 14:45 UTC. O upstream lento recebeu 224 requisições. running permaneceu 2, `scale_up` permaneceu 0, a fila permaneceu 0 e o p95 observado foi 0,05 s, com offered de 22,2 rps.

T58 passou em 2026-11-24 17:28 UTC no host `vps12052.integrator.host`, kernel 7.0.0-31, cgroup v2 puro, Go 1.26.0, PHP 8.5.4. O WSL2 continua sem permissão para criar quota; essa execução usou a VPS. O cgroup `/sys/fs/cgroup/eco11-e2e-3022687` recebeu `cpu.max` `70000 100000`. Durante a carga, `nr_throttled` acompanhou `nr_periods` (195 de 196 numa amostra). No fim, CPU global 1,000 com amostra válida, throttle 1 com amostra válida, e `worker_cpu_ratio` 0,640, acima da borda de subida 0,45. A fórmula pede dois workers. requested, admitted e running ficaram em 1, com `scale_up` 0 e `scale_down` 0. O contador de decisões não lista `cpu_saturation` porque o veto é um hold e hold não incrementa o contador. O encerramento não foi SIGKILL.

T59, PID 1940, 14:23 UTC. Com orçamento em que um worker cabe e o próximo não, `eregion_scaling_admission_denied_total` por `memory_headroom` chegou a 1, admitted ficou em 1, headroom em 0 e a memória corrente em 429744128 bytes. running no instante da barreira era 0. O encerramento do supervisor não foi SIGKILL.

T60, 15:30–15:33 UTC, 154,9 s. Na fase estável, offered ficou em 14,3 rps, o segundo worker subiu, a fila esvaziou, o p95 foi a 0,1 s e o probe entrou em `cooldown` com ganho marginal 0,1727, running 2 e admitted 2. Na fase seguinte, offered saiu de 14,3 rps, o estado do probe foi `rollback` e running voltou a 1 com draining 0. Nenhum dos dois encerramentos foi SIGKILL. O probe passou a contar uma amostra por época fechada: o reconcile de 1 s estava gravando a mesma janela de 10 s várias vezes e publicava ganho 0.

T63, PID 1658, limites gravados às 15:03:24 UTC antes do loop: teto de 15 min, janela de 10 s, passo 1, RSS de base 46744 kB e 8 threads. Os quatro ciclos usaram o mesmo PID. running ficou em 1, 2 ou 3 e admitted não passou de 3. O RSS ao voltar ao ocioso foi 53508, 54128, 54248 e 54044 kB, todos abaixo de 1,5 vez a base (70116 kB). O campo `failure` ficou vazio.

O overhead de `adaptive` com coletor ligado e probe desligado não entrou no benchmark. Entraram `scheduled` e `cpu_utilization` com min=max, que mantêm a instrumentação HTTP ligada. Uma comparação de alvos diferentes seria outro experimento, não overhead.

PUBLISHED continua BLOCKED. O protocolo permanece `eregion/1`. A junção local de `durin-presets` não é publicação.

## 6. Definition of Done

Fechado com evidência observada: T01–T53 e T55–T64. T62 conserva a série, o hash `d5a74b13c20e70a218b6f125164037a580de901b5a13857def5c1ff1be020996` e o PASS das três comparações.

Aberto: T54 BLOCKED até a publicação das versões compatíveis.

ECO-11 está DONE LOCAL.
