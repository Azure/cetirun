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
- [Public DNS and TLS](public-ingress/) — prepare one public hostname and automatic certificate
- [Policy-bound Teams](policy-teams/) — the cluster, admission and platform registration that Sandbox Agents must have
- [Agent Identity setup](agent-identity/) — encryption, workload trust, certificates, provider registrations and rollout

## Configuration

- [Configuration and skills](../concepts/configuration/) — shared defaults and instance overrides
- [Configure a team](../guides/teams/) — team context, provider defaults, and skills
- [Smart routing](smart-routing/) — routers, classifiers, and where requests went
- [Storage](storage/)
- [Istio authentication and egress](istio/)
- [Entra Agent Identity setup](entra-agent-identity/) — Blueprint, federation and Azure/Microsoft authorization
- [Identity provider setup](identity-providers/) — GitHub App, GitHub OAuth and Google OAuth

## Troubleshooting

Use [Troubleshooting](troubleshooting/) to identify problems with
reconciliation, storage, routing, and integrations. Find resource fields, Helm
values, and command options in [Reference](../reference/).

Do not paste full logs, Helm values, Secret output, or browser profiles into
public issues. Refer to [data and credential boundaries](../security/).
