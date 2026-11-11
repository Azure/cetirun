---
title: "Operations"
description: "Install CetiRun, configure shared services, and troubleshoot your deployment."
weight: 60
no_list: true
---

To do platform operations, you must have a reviewed environment and explicit
authority to change it. For a running instance, start with
[troubleshooting](troubleshooting/). When you prepare or change a deployment,
use the infrastructure guides.

The examples separate three types of steps: offline steps that compile or
render, connected checks, and deployment steps that change resources. A
successful render does not examine Azure feature availability. It also does
not examine a running cluster. Deployment helpers can write resources before
later checks fail. Before you use a deployment helper, read its prerequisites
and side effects.

## Install

- [Install CetiRun](../getting-started/platform/) — prerequisites and installation order
- [Infrastructure](infrastructure/) — AKS and Azure Blob NFS
- [Public DNS and TLS](public-ingress/) — configure one public hostname and automatic certificate through the platform deployment
- [Policy-bound Teams](policy-teams/) — the cluster, admission and platform registration that Sandbox Agents must have
- [Agent Identity setup](agent-identity/) — encryption, workload trust, certificates, provider registrations and rollout
- [Installation checklist](install-checklist/) — all installation steps in their order, with a check for each step
- [Plan capacity and cost](capacity/) — Agent resources, node counts, and the defaults that drive cost
- [Publish images and charts](artifacts/) — registry access, private registries, and restricted networks

## Configuration

- [Configuration and skills](../concepts/configuration/) — shared defaults and instance overrides
- [Configure a team](../guides/teams/) — team context, provider defaults, and skills
- [Smart routing](smart-routing/) — routers, classifiers, and where requests went
- [Skill Marketplace](skill-marketplace/) — approved skills from one Git repository for the Agents of policy-bound Teams
- [Monitoring and logs](observability/) — metrics, logs, and alerts, and their collection into Azure Data Explorer with ADX-Mon
- [Storage](storage/)
- [Istio authentication and egress](istio/)
- [Entra Agent Identity setup](entra-agent-identity/) — Blueprint, federation and Azure/Microsoft authorization
- [Identity provider setup](identity-providers/) — GitHub App, GitHub OAuth and Google OAuth
- [CetiAdmin](cetiadmin/) — the operations Agent that the operator installs by default, its access, and how to disable it
- [AI Gateway](ai-gateway/) — shared models, prices, Copilot proxies, credentials, logs, and model troubleshooting
- [Model access](models/) — which models each Agent gets, and its default model
- [Budgets and spend](budgets/) — Gateway budgets, request limits, and spend
- [Portal administration](portal-administration/) — the Admin pages of the Portal and their limits
- [Upgrade CetiRun](upgrade/) — upgrade the platform and the Agents, and the limits of a rollback
- [Make and restore backups](backup-restore/) — the data and Secrets that need a backup
- [Certificates and credentials](certificates/) — the source, renewal, and checks of each credential
- [Uninstall CetiRun](uninstall/) — the removal order, and the objects that stay
- [Administer Agents](agent-lifecycle/) — create, inspect, suspend, restart, migrate, update, and delete Agents
- [Enable Portal Chat](portal-chat/) — the private Chat path for Custom Agents

## Troubleshooting

Use [Troubleshooting](troubleshooting/) to identify problems with
reconciliation, storage, routing, and integrations. Find resource fields, Helm
values, and command options in [Reference](../reference/).

Do not paste full logs, Helm values, Secret output, or browser profiles into
public issues. Refer to [data and credential boundaries](../security/).
