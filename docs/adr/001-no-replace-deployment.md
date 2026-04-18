# ADR 001 — Kanary não substitui `Deployment`

- **Status:** Aceito
- **Data:** 2026-04-18
- **Autores:** Adriano Pavão

## Contexto

Soluções existentes (Flagger, Argo Rollouts) introduzem CRDs próprios (`Canary`, `Rollout`) que ou **envelopam** ou **substituem** o `Deployment` nativo do Kubernetes.
Essa escolha tem custos reais:

- Usuários têm que aprender um novo recurso como unidade primária de workload.
- Ferramentas consolidadas em volta de `Deployment` (dashboards, scripts, HPAs, pipelines) precisam ser adaptadas.
- Toda organização paga o custo de complexidade mesmo quando não precisa de rollout progressivo.

O SPEC do Kanary (requisito #1) exige expressamente trabalhar apenas com `Deployments` padrão.

## Decisão

O `Canary` do Kanary é um **recurso paralelo** que **referencia** um `Deployment` existente via `spec.targetRef`. O `Deployment` permanece a fonte canônica da intent do usuário.

Consequências práticas:

- O reconciler cria um **Deployment canário adicional** (cópia do template do alvo com 1 réplica por padrão), não altera o alvo diretamente durante o rollout.
- Services/Ingresses/Routes auxiliares são criados sob owner reference do `Canary` para garbage collection automática.
- Ao fim de um rollout bem-sucedido, o reconciler atualiza o template do `Deployment` estável e remove o canário.

## Consequências

Positivas:

- Adoção incremental: habilitar/desabilitar canary é feito via annotation (`kanary.io/canary-enabled=false`) sem remover o `Canary`.
- Compatibilidade total com ferramentas de observabilidade, HPA, PDB que já olham para `Deployment`.
- Onboarding mais simples para devs — eles continuam usando o recurso que já conhecem.

Negativas:

- Kanary não consegue evitar que alguém altere o `Deployment` à mão durante um rollout. Mitigação: validation webhook registra anomalias em events; docs orientam as equipes.
- Estratégias mais agressivas (como Blue/Green com dois ReplicaSets completos simultâneos) exigirão atenção extra no design futuro.

## Referências

- SPEC.md §2.1, §3.1, §4.3
- Flagger (https://flagger.app) — design compatível, mas sem substituição do Deployment.
- Argo Rollouts (https://argo-rollouts.readthedocs.io) — CRD próprio que substitui Deployment.
