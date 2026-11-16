# Operator manual verification

These steps check the Operator of an installed CetiRun platform on a live
cluster. They create a test Agent, check its resources and its route, and
delete it.

> **Warning:** These steps change resources in a live cluster. Before you
> start, get explicit authorization for the target cluster. Use a test cluster,
> not a shared or production cluster. These steps are not part of the offline
> checks in [DEVELOPMENT.md](../../../DEVELOPMENT.md).

For an automated lifecycle check, use [`scripts/e2e-test.sh`](../../../scripts/e2e-test.sh).
It pins the target cluster by its UID and does a safe cleanup. Refer to
[Application E2E lifecycle](../../../site/content/en/docs/reference/scripts.md#application-e2e-lifecycle).

## Prerequisites

| Requirement | Notes |
| --- | --- |
| An installed platform | Install CetiRun with `make deploy`. Refer to [Install CetiRun](../../../site/content/en/docs/getting-started/platform.md). The platform includes the Operator, the Portal, and the AI Gateway. Every Agent needs the AI Gateway. |
| Istio ingress | Necessary. The Operator routes each Agent through the Istio Gateway `aks-istio-ingress/cetirun-gateway` by default. |
| HOME storage | An Agent without `spec.sandboxRef` uses the StorageClass `default`, or a storage override. On AKS, `default` is Azure Disk with `WaitForFirstConsumer` binding. A Sandbox Agent uses the HOME storage of its Sandbox (`spec.storage.home`). |
| A kubectl context for the target | Use an explicit context in each command. |
| A Team, if the Agent selects a Sandbox | An Agent without `spec.sandboxRef` does not need a Team. A Sandbox Agent needs a Team that allows the Sandbox, and an Agent Policy that allows its model. Refer to [Policy-bound Teams](../../../site/content/en/docs/operations/policy-teams.md). |

Set the context. Then compare the UID of its `kube-system` namespace with the
UID that you got from your cluster inventory:

```bash
CONTEXT=<test-context>
kubectl --context "$CONTEXT" get namespace kube-system -o jsonpath='{.metadata.uid}'
```

## Step 1: Check the platform

```bash
kubectl --context "$CONTEXT" get crd agents.cetirun.microsoft.com \
  teams.cetirun.microsoft.com sandboxes.cetirun.microsoft.com \
  agentpolicies.cetirun.microsoft.com
kubectl --context "$CONTEXT" get deployment -n cetirun-system \
  cetirun-operator cetirun-portal cetirun-ai-gateway
kubectl --context "$CONTEXT" get gateways.networking.istio.io -n aks-istio-ingress cetirun-gateway
kubectl --context "$CONTEXT" logs -n cetirun-system deployment/cetirun-operator --tail=20
```

The commands use the default names and namespaces of `make deploy`. If your
installation uses other values, use those values. All deployments must be
available, and the Operator log must show no repeated errors.

## Step 2: Create a test Agent

Create a new Agent with your own alias. Use the Portal, as
[Create your first Agent](../../../site/content/en/docs/getting-started/first-instance.md)
describes, or the Kubernetes procedure in
[Create an Agent with Kubernetes](../../../site/content/en/docs/operations/agent-lifecycle.md#create-an-agent-with-kubernetes).
Select a new name, for example `manual-test`. Do not create the Agent
namespace or its Secrets in advance. The Operator creates them.

```bash
NAME=manual-test
```

## Step 3: Wait for the Agent

```bash
kubectl --context "$CONTEXT" wait --for=condition=Ready "agent/$NAME" --timeout=10m
kubectl --context "$CONTEXT" describe agent "$NAME"
```

The `Ready`, `PodReady`, and `IngressReady` conditions become `True`.
`status.accessUrl` is `https://<domain>/ui/<name>/`. The Agent resource is
cluster-scoped, so these commands do not use a namespace.

## Step 4: Check the resources

Read the namespace and the Helm release of the Agent from its status:

```bash
NAMESPACE=$(kubectl --context "$CONTEXT" get agent "$NAME" -o jsonpath='{.status.namespace}')
RELEASE=$(kubectl --context "$CONTEXT" get agent "$NAME" -o jsonpath='{.status.helmReleaseName}')
kubectl --context "$CONTEXT" get statefulset,pod,pvc,service,secret -n "$NAMESPACE"
helm --kube-context "$CONTEXT" status "$RELEASE" -n "$NAMESPACE"
kubectl --context "$CONTEXT" get virtualservice -n "$NAMESPACE" -o yaml
```

Compare the output with these names. They are the same for OpenClaw and
Hermes Agents. For the naming rules, refer to
[Architecture](../../../site/content/en/docs/concepts/architecture.md).

| Resource | Name |
| --- | --- |
| Namespace | `cetirun-<name>` |
| Helm release | `cetirun-<name>` |
| StatefulSet and Service | `cetirun-<name>` |
| Agent container | `cetirun` |
| Gateway token Secret | The name in `spec.gatewayTokenSecretRef` |
| VirtualService | `cetirun-<name>`, in the Agent namespace |

The VirtualService has one host, the domain of the installation. It routes
`/ui/<name>/` and the other paths of the Agent to the Service. It uses the
Istio Gateway of the installation. Do not print the values of the Secrets.

To read the Agent logs, run this command. Runtime logs can contain
conversation text. Do not share them in an issue or a pull request:

```bash
kubectl --context "$CONTEXT" logs -n "$NAMESPACE" "statefulset/cetirun-$NAME" -c cetirun --tail=50
```

## Step 5: Check the route and the access

1. Open `status.accessUrl` in a browser and sign in as the owner. Make sure
   that the Agent UI opens.
2. Send a request without a session. Make sure that the response is a redirect
   to the sign-in page, not the Agent UI:

   ```bash
   curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "https://<domain>/ui/$NAME/"
   ```

3. Sign in as a different user. Make sure that this user cannot open
   `/ui/<name>/` of your Agent.

To test the isolation between Agents, create a second test Agent for a second
owner. Make sure that each owner can open only the `/ui/<name>/` path of the
Agent that the owner owns. All Agents use the same host, so a request to a
different host is not an isolation test.

## Step 6: Delete the test Agent

Delete only the Agents that you created for this test. The command waits
until the Operator completes the cleanup and removes its finalizer:

```bash
kubectl --context "$CONTEXT" delete agent "$NAME" --timeout=10m
```

The Operator uninstalls the Helm release, and deletes the VirtualService and
the namespace. For the deletion behavior, refer to
[Delete only with a recovery plan](../../../site/content/en/docs/operations/agent-lifecycle.md#delete-only-with-a-recovery-plan).
The namespace deletion also deletes the PVCs. With the default Azure Disk
reclaim policy, the disks are also deleted. Save any test data that you need
before you delete the Agent.

> **Warning:** Do not delete the CetiRun CRDs to remove test resources. The
> CRDs are cluster-scoped. When you delete a CRD, Kubernetes deletes every
> object of that kind in the cluster: all Agents, Teams, Sandboxes, or
> Agent Policies. Do not delete the `default` StorageClass or shared skills
> storage.

## Troubleshooting

For what to examine after each kind of failure, refer to
[Follow the first failure](../../../site/content/en/docs/operations/troubleshooting.md#follow-the-first-failure).
A failed Helm install shows the `HelmDeployed` condition with the status
`False` and the reason `HelmInstallFailed`.

If the Agent stays in `Deleting`, examine the Operator logs, and make sure that
the Operator runs. Let the Operator complete the cleanup. Do not remove the
finalizer by hand. If you remove it, the Operator cannot remove the namespace,
the Helm release, and the AI Gateway key of the Agent.
