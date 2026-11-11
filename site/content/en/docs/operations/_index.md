---
title: "Operations"
description: "Install CetiRun on AKS, configure its shared services, and troubleshoot a deployment."
weight: 60
no_list: true
---

These pages are for platform administrators who install and run CetiRun. To
install, prepare the AKS cluster and the Microsoft Entra ID resources, and then
run `make deploy`. To use policy-bound Teams, also enable Sandbox execution.
For an Agent that does not start, begin with
[Troubleshooting](troubleshooting/).

Each page tells you which steps run offline, which steps read a live system,
and which steps change resources. Steps that change Azure or cluster resources
need the matching permissions. A local render shows only that the templates
are valid. The deployment helpers check Azure features and the running cluster
later. A helper can create resources before a later check fails, so read its
prerequisites first.

## Install

- [Install CetiRun](../getting-started/platform/) — prerequisites and
  installation order
- [Infrastructure](infrastructure/) — AKS, Azure Blob NFS, and the Azure Data
  Explorer resources of the ADX-Mon collection
- [Public DNS and TLS](public-ingress/) — one public hostname and an automatic
  certificate through the platform deployment
- [Policy-bound Teams](policy-teams/) — the cluster, admission, and platform
  registration that Sandbox Agents need
- [Prepare Agent connections](agent-identity/) — encryption, workload trust,
  certificates, provider registrations, and rollout
- [Installation checklist](install-checklist/) — all installation steps in
  their order, with a check for each step
- [Plan capacity and cost](capacity/) — Agent resources, node counts, and the
  defaults that drive cost
- [Publish images and charts](artifacts/) — registry access, private
  registries, and restricted networks

## Configuration

- [Configuration and skills](../concepts/configuration/) — how the operator
  merges Team settings, deployment values, and Agent overrides
- [Configure a team](../guides/teams/) — team context, Agent Policy and Sandbox
  bindings, smart-routing models, and skills
- [Smart routing](smart-routing/) — routers, classifiers, and where requests
  went
- [Skill Marketplace](skill-marketplace/) — approved skills from one Git
  repository for the Agents of policy-bound Teams
- [Monitoring and logs](observability/) — metrics, logs, and alerts, and their
  collection into Azure Data Explorer with ADX-Mon
- [Storage](storage/) — HOME volumes and shared skill storage
- [Istio authentication and egress](istio/) — sign-in, ownership checks, and
  egress audit logs
- [Managed Agent ingress mTLS](../guides/agent-ingress-mtls/) — TLS from the
  ingress gateway into the Pod of a Sandbox Agent
- [Prepare Microsoft Entra Agent Identity](entra-agent-identity/) — Blueprint,
  federation, and Azure and Microsoft authorization
- [Register GitHub and Google providers](identity-providers/) — GitHub App,
  GitHub OAuth, and Google OAuth
- [CetiAdmin](cetiadmin/) — the operations Agent that the operator installs by
  default, its access, and how to disable it
- [AI Gateway](ai-gateway/) — shared models, prices, Copilot proxies,
  credentials, logs, and model troubleshooting
- [Model access](models/) — which models each Agent gets, and its default model
- [Budgets and spend](budgets/) — Gateway budgets, request limits, and spend
- [Portal administration](portal-administration/) — the Admin pages of the
  Portal and their limits
- [Upgrade CetiRun](upgrade/) — upgrade the platform and the Agents, and the
  limits of a rollback
- [Make and restore backups](backup-restore/) — the data and Secrets that need
  a backup
- [Certificates and credentials](certificates/) — the source, renewal, and
  checks of each credential
- [Uninstall CetiRun](uninstall/) — the removal order, and the objects that
  stay
- [Administer Agents](agent-lifecycle/) — create, inspect, suspend, restart,
  migrate, update, and delete Agents
- [Enable Portal Chat](portal-chat/) — the private Chat path for Custom Agents

## Troubleshooting

Use [Troubleshooting](troubleshooting/) to find problems with reconciliation,
storage, routing, and integrations. Find resource fields, Helm values, and
command options in [Reference](../reference/).

Do not paste full logs, Helm values, Secret output, or browser profiles into
public issues. Refer to [Security](../security/).
