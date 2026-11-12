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

The checks are connected, read-only commands, unless the step says otherwise.
Replace the example names with the names of your environment.

## Before you start

| Do this | Page |
| --- | --- |
| Read the cluster requirements, the user names, the tools, and the permissions that each step needs | [Prepare the environment](../../getting-started/platform/#prepare-the-environment) |
| Compare the default node pools with the vCPU quota of the region | [Check the vCPU quota](../capacity/#check-the-vcpu-quota) |
| Select one cluster, domain, and environment file for each environment | [Environments and namespaces](../../getting-started/platform/#environments-and-namespaces) |

## Before the first make deploy

| Step | Do this | Page | Result | Check |
| --- | --- | --- | --- | --- |
| 1 | Provision AKS and connect to it. By default, the cluster template also creates the Azure Data Explorer resources of the ADX-Mon collection | [Infrastructure](../infrastructure/) and [Azure Data Explorer](../infrastructure/#azure-data-explorer) | The cluster, a kubeconfig context, and the `target` values. The outputs `adxClusterUri` and `adxIngestorClientId` give `observability.adx.endpoint` and `observability.ingestorClientId` | `kubectl auth whoami` and `kubectl get runtimeclass kata-vm-isolation` |
| 2 | Provision the Blob NFS storage of shared skills, and make sure that the StorageClasses exist | [Shared skills storage for `make deploy`](../storage/#shared-skills-storage-for-make-deploy) | The `sharedSkills` values and the StorageClass names | `kubectl get storageclass` |
| 3 | Examine the Kata node pools, and create the Sandbox HOME StorageClass | [Policy-bound Teams](../policy-teams/#the-cluster), steps 1 and 2 | The HOME StorageClass for Sandboxes | `kubectl get nodes -L agentpool` and `kubectl get storageclass` |
| 4 | Register the Portal sign-in application, assign the allowed groups, and add the federated credential. The user names in the allowed groups must give valid aliases | [Istio authentication](../istio/#prepare-identity-dns-and-tls) and [User names](../../getting-started/platform/#user-names) | `identity.tenantId`, `identity.portalClientId`, and `istio.values.allowedGroups` | `az ad app federated-credential list --id <portal-client-id>` |
| 5 | Configure DNS, and supply the ingress certificate or configure `publicTLS` | [Istio authentication](../istio/#prepare-identity-dns-and-tls) or [Public DNS and TLS](../public-ingress/) | `domain`, and the `cetirun-tls` Secret or the `publicTLS` section | `kubectl get secret cetirun-tls -n aks-istio-ingress`, unless you use `publicTLS` |
| 6 | Create the encryption identity, the Key Vault key, the Broker and MCP certificates, and the provider registrations | [Prepare Agent connections](../agent-identity/) | `identity.encryptionClientId`, `identity.keyVaultKeyUrl`, and the TLS Secrets of the Broker and MCP | The checks in steps 1 to 4 of that page |
| 7 | Create the Agent Identity Blueprint and the Authorization application, and federate the workloads | [Prepare Microsoft Entra Agent Identity](../entra-agent-identity/) | `identity.blueprintClientId`, `identity.authorizationClientId`, and `platform.values.cetirun-operator.azure.clientId` | `az ad app federated-credential list --id <blueprint-client-id>` |
| 8 | Issue the Sandbox admission certificate, and select the admission administrators | [Policy-bound Teams](../policy-teams/#the-admission-certificate), step 3 | The `sandboxAdmission` values | `kubectl get secret cetirun-sandbox-admission-tls -n cetirun-system` |
| 9 | Publish the images and charts, and give the nodes and the Operator access to the registry | [Publish images and charts](../artifacts/) | The `release` section and the digests of `customRuntime.approvedImages` | `make publish` ends without an error. For an ACR token of the Operator, step 3 of [Example: Azure Container Registry](../artifacts/#example-azure-container-registry) |
| 10 | Create the AI Gateway Secrets and, for `modelRouting.mode: auto`, the Decision-1 deployment | [Create the AI Gateway Secrets](../../getting-started/platform/#5-create-the-ai-gateway-secrets) | The Secret names and the `models` of the AI Gateway | `kubectl get secret gateway-provider-keys cetirun-ai-gateway-auth gateway-valkey-auth -n cetirun-system` |
| 11 | Write the environment file, and decide on the inputs that are difficult to change later | [Write the environment file](../../getting-started/platform/#6-write-the-environment-file) | The environment file, outside the repository | `make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml`, an offline command |

## Install and verify

| Step | Do this | Page | Check |
| --- | --- | --- | --- |
| 12 | Run `make deploy`. This step writes to the cluster | [Run the plan and the deployment](../../getting-started/platform/#platform-chart) | The command ends without an error. If it fails, refer to [When make deploy fails](../troubleshooting/#when-make-deploy-fails) |
| 13 | With `publicTLS`, run `make deploy-verify`. Then examine the releases, and send a test request to the AI Gateway | [Verify the deployment](../../getting-started/platform/#8-verify-the-deployment) | `make deploy-verify` reports `verified`, and `helm list` shows the three releases as `deployed` |
| 14 | Test a first Agent with two test accounts | [Test a first Agent](../../getting-started/platform/#test-a-first-agent) | Each test gives its expected result |
| 15 | Give users access | [Give users access](../../getting-started/platform/#9-give-users-access) | A user in an allowed group can sign in |

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
