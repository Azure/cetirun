---
title: "Installation checklist"
description: "All CetiRun installation steps in their order, with the page, the result, and the check of each step."
weight: 5
---

Use this checklist to install CetiRun with `make deploy`. The steps are in the
order of their dependencies. Each step names the page that describes it, the
result that later steps use, and a check. Most results are inputs of the
environment file of `make deploy`. [Install CetiRun](../../getting-started/platform/)
describes `make deploy` and the environment file.

Prepare the Azure and Entra dependencies first, then run `make deploy` to
install the platform. It does not create AKS, the shared-skills storage,
Entra applications, or the Key Vault key. The cluster helper can create the
ADX infrastructure; wait for its complete run, including Gateway API setup,
before installing the platform.

The checks are connected, read-only commands, unless the step says otherwise.
Replace the example names with the names of your environment. Use the private
kubeconfig for that environment for every Kubernetes and Helm command, and
pass the target subscription to every Azure resource command.

## Before you start

| Do this | Page |
| --- | --- |
| Select a published release and clone its tag | [Choose a release](../../getting-started/platform/#choose-a-release) |
| Read the cluster requirements, the user names, the tools, and the permissions that each step needs | [Prepare the environment](../../getting-started/platform/#prepare-the-environment) |
| Compare the default node pools with the vCPU quota of the region | [Check the vCPU quota](../capacity/#check-the-vcpu-quota) |
| Select one cluster, domain, and environment file for each environment | [Environments and namespaces](../../getting-started/platform/#environments-and-namespaces) |
| Record and verify the target subscription/tenant and original default contexts. Prepare a private kubeconfig and configuration directory outside the repository | [Select the Azure target](../infrastructure/#select-the-azure-target) and [Connect to the cluster](../infrastructure/#connect-to-the-cluster) |
| For a test environment, set both minimum and maximum pool counts and account for admission replica placement | [Small test environment](../infrastructure/#small-test-environment) |

Reuse configuration structure across environments, but create the identities,
federated credentials, encryption resources, and installation credentials for
the new environment. Model-service access can be shared when its owner has
authorized that use. Replace all example IDs, image references, and resource
names with verified inputs. Keep secrets and environment-specific deployment
records outside the repository.

## Before the first make deploy

| Step | Do this | Page | Result | Check |
| --- | --- | --- | --- | --- |
| 1 | Provision AKS and connect to it. By default, the cluster template also creates the Azure Data Explorer resources of the ADX-Mon collection | [Infrastructure](../infrastructure/) and [Azure Data Explorer](../infrastructure/#azure-data-explorer) | The cluster, a kubeconfig context, and the `target` values. The outputs `adxClusterUri` and `adxIngestorClientId` give `observability.adx.endpoint` and `observability.ingestorClientId` | `kubectl auth whoami` and `kubectl get runtimeclass kata-vm-isolation` |
| 2 | Provision the Blob NFS storage of shared skills, and make sure that the StorageClasses exist | [Shared skills storage for `make deploy`](../storage/#shared-skills-storage-for-make-deploy) | The `sharedSkills` values and the StorageClass names | `kubectl get storageclass` |
| 3 | Examine the Kata node pools, and create the Sandbox HOME StorageClass | [Policy-bound Teams](../policy-teams/#the-cluster), steps 1 and 2 | The HOME StorageClass for Sandboxes | `kubectl get nodes -L agentpool` and `kubectl get storageclass` |
| 4 | Register the Portal sign-in application, assign the allowed groups, and add the federated credential. The user names in the allowed groups must give valid aliases | [Istio authentication](../istio/#prepare-identity-dns-and-tls) and [User names](../../getting-started/platform/#user-names) | `identity.tenantId`, `identity.portalClientId`, and `istio.values.allowedGroups` | `az ad app federated-credential list --id <portal-client-id>` |
| 5 | Configure DNS, and supply the ingress certificate or configure `publicTLS` | [Istio authentication](../istio/#prepare-identity-dns-and-tls) or [Public DNS and TLS](../public-ingress/) | `domain`, and the `cetirun-tls` Secret or the `publicTLS` section | `kubectl get secret cetirun-tls -n aks-istio-ingress`, unless you use `publicTLS` |
| 6 | Create the encryption identity, the Key Vault key, the Agent Identity Broker and MCP certificates, and the provider registrations | [Prepare Agent connections](../agent-identity/) | `identity.encryptionClientId`, `identity.keyVaultKeyUrl`, and the TLS Secrets of the Broker and MCP | The checks in steps 1 to 4 of that page |
| 7 | Create the Agent Identity Blueprint and the Authorization application, and federate the workloads | [Prepare Microsoft Entra Agent Identity](../entra-agent-identity/) | `identity.blueprintClientId`, `identity.authorizationClientId`, and `platform.values.cetirun-operator.azure.clientId` | `az ad app federated-credential list --id <blueprint-client-id>` |
| 8 | Issue the Sandbox admission certificate, and select the admission administrators | [Policy-bound Teams](../policy-teams/#the-admission-certificate), step 3 | The `sandboxAdmission` values | `kubectl get secret cetirun-sandbox-admission-tls -n cetirun-system` |
| 9 | Check access to the release. Publish only for a custom build or private registry | [Use official MCR artifacts](../artifacts/#use-official-mcr-artifacts) | The `release` section and the digests of `customRuntime.approvedImages` | The inspection commands on that page succeed |
| 10 | Create the AI Gateway Secrets and, for `modelRouting.mode: auto`, the Decision-1 deployment | [Create the AI Gateway Secrets](../../getting-started/platform/#5-create-the-ai-gateway-secrets) | The Secret names and the `models` of the AI Gateway | `kubectl get secret gateway-provider-keys cetirun-ai-gateway-auth gateway-valkey-auth -n cetirun-system` |
| 11 | Write the environment file, and decide on the inputs that are difficult to change later | [Write the environment file](../../getting-started/platform/#6-write-the-environment-file) | The environment file, outside the repository | `make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml`, an offline command |

## Install and verify

| Step | Do this | Page | Check |
| --- | --- | --- | --- |
| 12 | Run `make deploy`. This step writes to the cluster | [Run the plan and the deployment](../../getting-started/platform/#platform-chart) | The command ends without an error. If it fails, refer to [When make deploy fails](../troubleshooting/#when-make-deploy-fails) |
| 13 | With `publicTLS`, run `make deploy-verify`. Then examine the releases, and send a test request to the AI Gateway | [Verify the deployment](../../getting-started/platform/#8-verify-the-deployment) | `make deploy-verify` reports `verified`, and `helm list` shows the three releases as `deployed` |
| 14 | Test a first Agent with two test accounts | [Test a first Agent](../../getting-started/platform/#test-a-first-agent) | Each test gives its expected result |
| 15 | Give users access | [Give users access](../../getting-started/platform/#9-give-users-access) | A user in an allowed group can sign in |

Run the plan, deployment, and verification sequentially. `make deploy-verify`
refuses an active Helm operation. With `publicTLS`, it checks public DNS, the
served TLS certificate, the sign-in redirect and PKCE parameters, health,
readiness, and unauthenticated API protection. It does not complete a browser
login or send an Agent conversation.

Before handoff, record these separate acceptance results:

- The deployed release revisions, current workload readiness, and actual
  image identities against the selected release artifacts.
- Synthetic Agent creation, the required Kata runtime, Bound HOME claims,
  and read-only shared-skills mounts. If Custom is enabled, include an
  approved Custom runtime image.
- Model calls using the Agent's own model grant. Test `auto` separately when
  configured. A model call does not establish native chat or tool access.
- An owner-authenticated browser session and a separate account's access
  denial, as [Test a first Agent](../../getting-started/platform/#test-a-first-agent)
  describes. Mark these untested when no user session is available.
- [Suspend and resume](../agent-lifecycle/#suspend-and-resume-an-agent) of a
  synthetic Agent, including completed Pod termination, route withdrawal,
  and preserved HOME/PVC identity after resume. A content persistence claim
  also needs a bounded write/read check using synthetic data.
- Actual [log and metric ingestion](../observability/) and collection
  health. Ready collector Pods alone do not prove ingestion.

Keep the source SHA, artifact identities, private configuration, commands and
results, release history, recovery steps, and checks not run. Verify that the
default Azure subscription/tenant and global Kubernetes context are unchanged.

## After the first make deploy

These steps are optional. Do them in this order:

| Do this | Page |
| --- | --- |
| Register the platform, label the peers, and enable Sandbox execution for policy-bound Teams | [Policy-bound Teams](../policy-teams/#the-platform-registration), steps 4 to 7 |
| Configure Team routers | [Smart routing](../smart-routing/) |
| Give the Agents of policy-bound Teams approved skills | [Skill Marketplace](../skill-marketplace/) |

Before you serve users, also plan the operations after the installation:
[Monitoring and logs](../observability/),
[Make and restore backups](../backup-restore/),
[Certificates and credentials](../certificates/), and
[Upgrade CetiRun](../upgrade/).
