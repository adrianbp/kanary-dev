# Kanary Operator

> Kubernetes Operator para canary deployments **simples, previsíveis e multi-cloud**, construído em Go.
> Simple, predictable, multi-cloud canary deployments for Kubernetes.

[![CI](https://github.com/adrianbp/kanary-dev/actions/workflows/ci.yaml/badge.svg)](https://github.com/adrianbp/kanary-dev/actions/workflows/ci.yaml)

---

## Visão / Overview

Kanary é um operador **leve** que adiciona canary deployments sobre `Deployment` padrão do Kubernetes — sem substituir o recurso nativo, sem exigir service mesh, e com provedores plugáveis de tráfego (ingress-nginx, OpenShift Route) e de métricas (Prometheus, Datadog, Dynatrace).

A especificação completa está em [SPEC.md](./SPEC.md); o backlog priorizado em [BACKLOG.md](./BACKLOG.md).

## Início rápido / Quickstart

Pré-requisitos: **Go 1.22+** (recomendado 1.23), Docker, `kubectl`, um cluster (Docker Desktop ou kind).

```bash
# 0. Primeira vez: resolve dependências (go.sum é criado aqui)
go mod tidy

# 1. Gera deepcopy, CRDs e RBAC
make generate
make manifests

# 2. Instala os CRDs no cluster atual
make install

# 3. Roda o controller localmente apontando para o kubecontext corrente
make run
```

Em outro terminal, aplique um `Canary` de exemplo (após criar um Deployment `checkout-api` e um Ingress `checkout-api`):

```yaml
apiVersion: kanary.io/v1alpha1
kind: Canary
metadata:
  name: checkout-api
  namespace: default
spec:
  targetRef:
    kind: Deployment
    name: checkout-api
  service:
    port: 8080
  trafficProvider:
    type: nginx
    ingressRef:
      name: checkout-api
  strategy:
    mode: Manual
    steps:
      - weight: 10
      - weight: 50
      - weight: 100
```

Promova cada step com uma annotation:

```bash
kubectl annotate canary checkout-api kanary.io/promote=true --overwrite
```

## Desenvolvimento / Development

| Target            | O que faz                                                 |
|-------------------|------------------------------------------------------------|
| `make test`       | `go test` com race + coverage; falha se < 80%.            |
| `make lint`       | `golangci-lint` com o mesmo preset do CI.                  |
| `make build`      | Binário estático em `bin/manager`.                         |
| `make docker-build` | Imagem distroless multi-stage.                          |
| `make run`        | Executa o controller localmente (usa kubecontext atual).   |

## Estrutura

```
api/v1alpha1/             # Tipos + zz_generated.deepcopy.go (CRD)
cmd/manager/              # Entrypoint do binário
internal/
  controller/             # Canary reconciler
  domain/                 # Tipos de domínio puros (sem deps K8s)
  errors/                 # Sentinelas + helper Retryable
  traffic/                # Interface Router + impls (nginx, openshift)
  metrics/                # Interface Provider + impls (prometheus, …)
  analysis/               # Engine de análise (M3)
charts/kanary/            # Helm chart (M1)
config/                   # Manifests gerados pelo controller-gen
.github/                  # Workflows + composite actions
docs/                     # ADRs e guias
```

Alinhado com o layout descrito em *Go in Action, 2nd Ed.*, capítulo 11 — evita `src/` e usa `internal/` para código não exportado.

## Roadmap

Ver [SPEC.md §12](./SPEC.md#12-roadmap--github-project-requisito-13). Milestones ativos:

- **M1** — MVP Manual + Nginx + Helm + CI
- **M2** — OpenShift Routes + E2E EKS/AKS/OCP
- **M3** — Progressive + análise de métricas
- **M4** — Hardening, SBOM, 1.0
- **M5** — Blue/Green (futuro)

## Licença

Apache 2.0. Veja [LICENSE](./LICENSE).
