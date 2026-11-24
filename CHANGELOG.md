# Changelog

## 0.4.5 — ECO-11

Estratégias opt-in `cpu_utilization`, `latency`, `throughput`, `hybrid`, `scheduled` e `adaptive`. A admissão continua no planner e no ledger já existentes. Sem configuração nova, a captura extra de sinais fica desligada e `fixed`, `resources` e `backlog` mantêm o cooldown padrão de 1s. `cpu_utilization` e `adaptive` exigem leitura de CPU por PID e falham em `check` quando a plataforma não oferece essa leitura. O protocolo continua `eregion/1`. Ver [`docs/scaling-strategies.md`](docs/scaling-strategies.md).

## 0.4.4 — Resource Guard

Ver [`docs/releases/0.4.4.md`](docs/releases/0.4.4.md).

O protocolo continua `eregion/1`. Nenhuma estratégia de scaling foi adicionada e os limites de admissão não foram afrouxados para passar o gate.

### Admissão

- O modo padrão é `observe`. `enforce` recusa processos novos que não cabem no orçamento. `off` mantém o reconciler anterior.
- `hard_limit_mb` é um orçamento de admissão. Não é um limite físico do sistema operacional.
- O ledger reserva o custo projetado antes do spawn, troca essa reserva pelo RSS medido na amostra seguinte e bloqueia só admissões novas quando o RSS dos workers já admitidos cresce. Workers em execução não são interrompidos por essa conta.

### Evidência

- Matriz ECO09-T01–T38 e o ciclo reserva → RSS → crescimento estão cobertos por testes em `internal/scaling`, `internal/resources`, `internal/worker`, `internal/config` e `internal/telemetry`.
- O gate de 20 pares de 60 s em Linux deu PASS, com o par discrepante mantido na série. A análise sem esse par está em [`docs/eco-09/gate-sensitivity.json`](docs/eco-09/gate-sensitivity.json) e não substitui o resultado oficial.
- O binário de medição não importa `net/http/pprof`. O listener de perfil exige `-tags pprof`.

## 0.4.2

Ver [`docs/releases/0.4.2.md`](docs/releases/0.4.2.md). O contrato publicado dessa tag não inclui o Resource Guard.
