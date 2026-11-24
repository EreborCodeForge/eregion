# Estratégias de escala (ECO-11)

As estratégias novas são opt-in. `fixed`, `resources` e `backlog` continuam com os mesmos alvos e com `scale_up_cooldown` padrão de 1s. O planner e o ledger do Resource Guard continuam sendo a única admissão. A estratégia só calcula a demanda.

`resource_guard.mode: off` recusa estratégia nova. `observe` e `enforce` aceitam as estratégias novas; `adaptive` exige `enforce`. Sem configuração nova, a captura extra de HTTP fica desligada.

## Sinais

Cada avaliação usa uma janela fechada de 10s a 120s, alinhada ao tick de 1s que já existe. Sinal ausente, velho, zerado por reset ou estourado fica inválido. Zero só entra na conta quando foi medido.

CPU por workload soma `utime` e `stime` dos PIDs do pool no Linux. Reuso de PID, saída no meio do intervalo ou troca de geração invalidam o intervalo. A CPU global continua sendo a do cgroup e não é dividida pelo número de workers. Fora do Linux, `worker_cpu` fica `unsupported` e `cpu_utilization` / `adaptive` falham em `eregion check` e no start.

## Fórmulas

`cpu_utilization` pede `ceil(N * utilização / alvo)` quando a utilização sai da banda. Dentro da banda, o alvo fica onde está.

`latency` só sobe um worker quando o p95 está alto, há pressão de fila e os workers estão ocupados. p95 alto sem fila produz `latency_without_queue_pressure` e não escala. Menos de 100 amostras, ou um bucket que atravessa o limiar, não autoriza a subida.

`throughput` usa o RPS oferecido, inclusive rejeições locais: `ceil(oferecido / target_rps_per_worker)`. Esse alvo precisa ser calibrado no serviço real. Um valor alto demais segura workers abaixo da demanda; um valor baixo demais pede workers que o guard pode recusar.

`hybrid` HTTP escolhe o maior pedido de subida válido e a descida mais conservadora. Os pedidos não são somados. Ociosidade completa é a exceção e pode descer. `hybrid` consumer reutiliza `ceil(backlog * duração_média / tempo_de_dreno)` e exige o provider da ECO-03.

`scheduled` é um calendário UTC de intervalos semiabertos `[início, fim)`, com no máximo 32 janelas. Não há cron e um salto de relógio não reexecuta a janela perdida.

`adaptive` é uma máquina de estados em memória, com um probe `+1` por supervisor. O rollback volta pelo drain normal. Nada é gravado no YAML. Probe bloqueado pelo guard não conta como ganho.

## Controle

Confirmações, passo e veto de expansão ficam separados da histerese legada. Para estratégia nova omitida, a janela é 10s, o cooldown de subida é 10s e a espera de descida é 30s. Se `window` for maior que o cooldown, o controle usa o maior dos dois. Sinal inválido segura o alvo atual. Um pedido recusado pelo guard não consome a confirmação de sucesso.

A expansão é vetada quando a CPU global é desconhecida ou está em 0,90 ou mais, quando o throttle está em 0,20 ou mais, ou quando a razão de 5xx não local chega a 0,05. Rejeição local de fila não veta.

## Telemetria

O scrape lê o cache publicado no reconcile. Não amostra de novo. `eregion_scaling_policy_target_workers` continua sendo o alvo H. Séries novas de sinal, confirmação, decisão e probe usam labels fechados.

`eregion check` imprime `strategy=` e `worker_cpu=supported|unsupported`. O doctor separa `configured`, `supported` e `observed`. `observed` só existe com processo em execução; o check não prova observação.

## Kubernetes

Estas estratégias calculam a demanda dentro do processo. Elas não substituem um HPA. Quem já escala réplicas por CPU ou RPS deve manter um único dono da contagem de workers.

## Exemplo

O fragmento abaixo é o bloco `scaling`, sem `strategy` e sem `resource_guard`. A Forge grava a estratégia escolhida na CLI e encaminha este texto.

```yaml
throughput:
  target_rps_per_worker: 50
scale_up_cooldown: 10s
scale_down_idle_for: 30s
```

Outros fragmentos estão em `docs/examples/`.
