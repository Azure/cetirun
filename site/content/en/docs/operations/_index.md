---
title: "Operations"
description: "Install CetiRun, configure shared services, and troubleshoot your deployment."
weight: 60
no_list: true
---

Platform operations require a reviewed environment and explicit authority to
change it. Begin with [troubleshooting](troubleshooting/) for a running instance;
use the infrastructure guides when preparing or changing a deployment.

Examples distinguish offline compilation/rendering, connected verification,
and mutating deployment. A successful render checks neither Azure feature
availability nor a running cluster. Deployment helpers can write resources
before later checks fail; read their prerequisites and side effects first.

## Install

- [Install CetiRun](../getting-started/platform/) — prerequisites and installation order
- [Infrastructure](infrastructure/) — AKS and Azure Blob NFS
- [Policy-bound Teams](policy-teams/) — the cluster, admission and platform registration that Sandbox Agents need
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
reconciliation, storage, routing, and integrations. Look up resource fields,
Helm values, and command options in [Reference](../reference/).

Do not paste full logs, Helm values, Secret output, or browser profiles into
public issues. See [data and credential boundaries](../security/).
