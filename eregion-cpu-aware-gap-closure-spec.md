# Eregion — CPU-Aware Final Gap Closure Specification

**Repository:** `EreborCodeForge/eregion`  
**Target:** `main`  
**Context:** complemento final da implementação `CPU-Aware Resource Detection & Worker Sizing Advisor`  
**Purpose:** fechar os gaps remanescentes identificados após a implementação da Phase 1 + Phase 1.1  
**Nature:** hardening / correctness / test coverage  
**Compatibility:** não quebrar contrato atual, não adicionar autosizing, não alterar `workers.count`

## 1. Objetivo

Esta spec define exclusivamente os pontos ainda abertos após a implementação inicial do resource detector e worker sizing advisor.

O core atual deve ser preservado:

```text
detect
→ analyze
→ log
→ metrics
→ start configured workers
```

Continuam proibidos nesta rodada:

```text
auto-resize
dynamic workers
CPU pinning
GOMAXPROCS mutation
YAML sizing config
Kubernetes API
Docker API
external dependencies
```

## 2. Prioridades

### P0 — correctness em containers/Kubernetes
- resolver o cgroup efetivo do processo;
- não assumir que a quota está diretamente no root de `/sys/fs/cgroup`.

### P1 — semântica de diagnóstico
- corrigir `CPUQuotaDetected`;
- separar source de quota, cpuset e fallback;
- não inferir container apenas por cgroup/cpuset.

### P2 — observabilidade
- registrar fallback de resource detection em DEBUG;
- manter ausência normal de quota fora de WARN.

### P3 — cobertura
- completar matriz explícita de testes;
- teste do cgroup leaf;
- teste garantindo soberania de `workers.count`.

## 3. Problema principal — cgroup root pode não representar o processo

Ler apenas:

```text
/sys/fs/cgroup
```

pode ser incorreto em Kubernetes, Docker, containerd, systemd scopes e nested cgroups.

Exemplo:

```text
/sys/fs/cgroup/cpu.max
=> max 100000

/proc/self/cgroup
=> 0::/kubepods.slice/.../pod123/container456

/sys/fs/cgroup/kubepods.slice/.../pod123/container456/cpu.max
=> 200000 100000
```

Nesse caso:

```text
root cpu.max => unlimited
process cgroup => 2 CPUs
```

O Eregion deve usar o cgroup efetivo do próprio processo.

## 4. Fluxo esperado

```text
read /proc/self/cgroup
        ↓
resolve current cgroup membership
        ↓
join with /sys/fs/cgroup
        ↓
read effective cpu/memory/cpuset files
        ↓
fallback safely when unavailable
```

## 5. cgroup v2

Formato comum:

```text
0::/kubepods.slice/kubepods-burstable.slice/...
```

Extrair o path e resolver dentro de `/sys/fs/cgroup`.

Implementação deve normalizar o path e impedir escape do root.

Arquivos do leaf:

```text
cpu.max
cpuset.cpus.effective
cpuset.cpus
memory.max
```

## 6. Segurança de path

Normalizar:

```go
clean := filepath.Clean("/" + rawPath)
relative := strings.TrimPrefix(clean, "/")
resolved := filepath.Join(root, relative)
```

Validar que `resolved` permanece dentro do root usando `filepath.Rel`.

Se escapar:

```text
fallback seguro
DEBUG diagnostic
```

Nunca falhar startup.

## 7. cgroup v1

`/proc/self/cgroup` pode conter:

```text
2:cpu,cpuacct:/docker/abc
3:memory:/docker/abc
4:cpuset:/docker/abc
```

Resolver separadamente os controllers:

```text
cpu
memory
cpuset
```

Modelo interno sugerido:

```go
type CgroupMembership struct {
    Version int
    Unified string
    CPU     string
    Memory  string
    CPUSet  string
}
```

## 8. Parser puro

Criar:

```go
func ParseSelfCgroup(data []byte) CgroupMembership
```

Sem filesystem interno.

Cobrir cgroup v1, v2, malformed input e input vazio.

## 9. Fallback

Se `/proc/self/cgroup` estiver ausente, inválido ou ilegível:

```text
usar comportamento root atual
→ depois GOMAXPROCS
→ depois NumCPU
→ depois 1
```

Registrar somente DEBUG.

## 10. Semântica de detection source

`CPUQuotaDetected` só pode ser `true` quando uma quota real foi detectada.

Cpuset deve ter sinalização própria:

```go
CPUQuotaDetected bool
CPUSetDetected   bool
```

Adicionar source semântica:

```go
type CPUDetectionSource string

const (
    CPUDetectionCgroupQuota CPUDetectionSource = "cgroup_quota"
    CPUDetectionCPUSet      CPUDetectionSource = "cpuset"
    CPUDetectionCombined    CPUDetectionSource = "quota_cpuset"
    CPUDetectionGOMAXPROCS  CPUDetectionSource = "gomaxprocs"
    CPUDetectionNumCPU      CPUDetectionSource = "numcpu"
    CPUDetectionFallback    CPUDetectionSource = "fallback"
)
```

## 11. Regras da source

```text
quota somente
=> cgroup_quota

cpuset somente
=> cpuset

quota + cpuset
=> quota_cpuset

sem ambos + GOMAXPROCS
=> gomaxprocs

fallback NumCPU
=> numcpu
```

## 12. CPU efetiva

Quando quota e cpuset existirem:

```text
availableCPU = min(quota, cpuset)
```

Casos obrigatórios:

```text
quota=2, cpuset=1 => 1
quota=1, cpuset=4 => 1
```

## 13. `cpu.max = max`

```text
max 100000
```

significa quota ilimitada.

Não transformar em zero.

Se cpuset existir:

```text
cpu.max=max
cpuset=0-1
=> availableCPU=2
=> CPUQuotaDetected=false
=> CPUSetDetected=true
=> Source=cpuset
```

## 14. GOMAXPROCS

Continua sendo:

- métrica;
- fallback;
- diagnóstico.

Não usar:

```text
min(cgroup quota, GOMAXPROCS)
```

para sizing dos workers PHP.

Exemplo válido:

```text
cgroup CPU=4
GOMAXPROCS=2
AvailableCPUs=4
```

Os workers PHP não obedecem `GOMAXPROCS`.

## 15. Environment

Não inferir:

```text
cpuset exists => container
```

Preferir representar mecanismo detectado:

```text
cgroup-v1
cgroup-v2
host
unknown
```

O campo deve continuar apenas diagnóstico e nunca afetar sizing.

## 16. Logs de fallback

Usar DEBUG para:

- `/proc/self/cgroup` ausente;
- arquivo cgroup ausente;
- permission denied;
- quota ilimitada;
- parser inválido;
- fallback para root;
- fallback para GOMAXPROCS;
- fallback para NumCPU.

Não usar WARN para ausência normal de quota.

WARN permanece reservado para sizing realmente extremo.

## 17. Testes obrigatórios

### Quota + cpuset

```text
quota=2 + cpuset=1
=> AvailableCPUs=1
=> CPUQuotaDetected=true
=> CPUSetDetected=true
=> Source=quota_cpuset
```

```text
quota=1 + cpuset=4
=> AvailableCPUs=1
```

### Unlimited + cpuset

```text
cpu.max=max
cpuset=0-1
=> AvailableCPUs=2
=> CPUQuotaDetected=false
=> CPUSetDetected=true
=> Source=cpuset
```

### Fallback GOMAXPROCS

```text
quota unavailable
cpuset unavailable
GOMAXPROCS=2
NumCPU=16

=> AvailableCPUs=2
=> Source=gomaxprocs
```

### CPU fracionária

```text
AvailableCPUs=0.25
ConfiguredWorkers=1

=> RecommendedMin >= 1
=> Recommended >= 1
=> RecommendedMax >= 1
=> WorkersPerCPU=4
```

Manter também caso `0.5`.

### CPU inválida

```text
AvailableCPUs=0
GOMAXPROCS=0
NumCPU=0

=> fallback final = 1
```

## 18. Teste obrigatório — cgroup leaf v2

Fake filesystem:

```text
/proc/self/cgroup
0::/kubepods/pod1/container1
```

Root:

```text
/sys/fs/cgroup/cpu.max
max 100000
```

Leaf:

```text
/sys/fs/cgroup/kubepods/pod1/container1/cpu.max
200000 100000
```

Esperado:

```text
AvailableCPUs=2
```

Esse teste é obrigatório porque valida o caso real de Kubernetes.

## 19. Teste de memory leaf

```text
/sys/fs/cgroup/kubepods/pod1/container1/memory.max
1073741824
```

Esperado:

```text
MemoryLimitKnown=true
MemoryLimitBytes=1073741824
```

## 20. Leaf ausente / malformed

Se membership existir mas leaf não:

```text
fallback seguro
sem panic
sem startup failure
```

Se `/proc/self/cgroup` estiver malformed:

```text
fallback root/runtime
```

## 21. `workers.count` continua soberano

Caso:

```text
AvailableCPU=1
ConfiguredWorkers=16
```

Advisor:

```text
Warning=true
Recommended=2
RecommendedMax=4
```

Mas obrigatoriamente:

```text
cfg.Workers.Count == 16
pool Desired == 16
```

Se possível, testar wiring sem precisar subir 16 PHP workers reais.

## 22. Não alterar thresholds

Manter classificação atual:

```text
<=1      cpu_conservative
<=2      balanced
<=4      io_optimized
<=8      high_oversubscription
>8       extreme_oversubscription
```

Sem recalibração antes de benchmark.

## 23. Não alterar fórmula

Manter:

```text
min = ceil(CPU × 1)
recommended = ceil(CPU × 2)
max = ceil(CPU × 4)
```

Sempre com mínimo 1.

## 24. Não implementar nesta rodada

Não adicionar:

```text
runtime CPU usage sampling
adaptive recommendation
dynamic worker resizing
CPU pinning
GOMAXPROCS mutation
Kubernetes API
Docker API
YAML sizing config
auto-enforcement
```

## 25. README

Documentar:

```text
Eregion resolves the current process cgroup, not only the cgroup root.
```

E:

```text
GOMAXPROCS does not cap PHP worker processes.
```

## 26. Startup log recomendado

```text
INFO runtime resources detected
cpu_logical=16
cpu_available=2
gomaxprocs=2
cpu_detection_source=quota_cpuset
cgroup_version=2
memory_limit_mb=1024
```

Não logar raw cgroup path por padrão.

## 27. Definition of Done

- [ ] `/proc/self/cgroup` é parseado;
- [ ] cgroup v2 leaf é resolvido;
- [ ] cgroup v1 controllers são resolvidos;
- [ ] CPU quota usa o cgroup efetivo do processo;
- [ ] cpuset usa o cgroup efetivo;
- [ ] memory limit usa o cgroup efetivo;
- [ ] fallback para root/runtime permanece seguro;
- [ ] quota + cpuset usa o menor valor;
- [ ] `cpu.max=max` não vira quota zero;
- [ ] `CPUQuotaDetected` representa quota real;
- [ ] cpuset possui sinalização própria;
- [ ] CPU detection source é semanticamente correta;
- [ ] `Environment` não chama host de container apenas por cpuset;
- [ ] fallback possui DEBUG útil;
- [ ] ausência normal de quota não gera WARN;
- [ ] CPU 0.25 possui teste;
- [ ] CPU 0.5 possui teste;
- [ ] quota2/cpuset1 possui teste;
- [ ] quota1/cpuset4 possui teste;
- [ ] max/cpuset possui teste;
- [ ] fallback GOMAXPROCS possui teste;
- [ ] cgroup leaf possui teste;
- [ ] malformed membership possui teste;
- [ ] `workers.count` continua soberano;
- [ ] nenhum YAML novo;
- [ ] nenhum autosizing;
- [ ] nenhuma dependência externa.

## 28. Resultado esperado

Após esta rodada, o detector deve ser confiável em:

```text
bare metal
VM
Docker
Kubernetes
cgroup v1
cgroup v2
cpuset-only
quota-only
quota + cpuset
no-limit environments
```

e continuar atuando somente como:

```text
detect
→ recommend
→ observe
```

sem alterar a intenção explícita da configuração.

## 29. Regra final para o agent

O objetivo é corrigir **detecção e semântica**, não aumentar automação.

O Eregion não deve virar scheduler de CPU.

O sistema operacional e os cgroups continuam responsáveis pelo enforcement.

O Eregion precisa apenas detectar com boa precisão os recursos efetivos, recomendar sizing e tornar o diagnóstico observável.
