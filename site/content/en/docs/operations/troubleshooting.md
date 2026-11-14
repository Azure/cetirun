---
title: "Troubleshooting"
description: "Find if a deployment step, a platform component, reconciliation, storage, routing, or an external integration fails."
weight: 40
---

This page is for platform administrators. It covers failures of `make deploy`,
of the platform components, and of the Agents that the Operator reconciles.
For other problems, use these pages:

- [Troubleshoot your Agent](../../guides/troubleshooting/) for the problems
  that an Agent owner can solve in the Portal
- [Troubleshoot model requests](../ai-gateway/#troubleshoot-model-requests)
  for failed model calls through the AI Gateway

The checks on this page connect to Kubernetes, but they do not change it,
unless a step says otherwise. Do not paste their output into public issues,
because it can contain names, identities, or Secret values.

## When make deploy fails

`make deploy` does not show the output of the tools that it runs, because that
output can contain rendered Secrets. When a step fails, the message names the
command and its exit status, for example:

```text
ERROR: kubectl rollout status deployment/cetirun-portal --namespace cetirun-system --timeout=600s failed with exit 1; deployment did not complete
```

Run a read-only command for the named object yourself, for example
`kubectl get` for the workload or `helm status` for the release. Other messages come from the checks
of `make deploy` itself. These messages name the input or the object that
failed, for example
`Required Secret key is missing: cetirun-system/gateway-provider-keys:AZURE_OPENAI_KEY`.

`make deploy` runs these stages in order. Without `publicTLS`, the first three
stages do not write to the cluster.

| Stage | Example messages | What to do |
| --- | --- | --- |
| Input validation | `... is required and must be a nonempty string`, `... conflicts with the complete deployment inputs`, `Invalid YAML at line 12, column 3; input contents withheld` | Correct the environment file. Run `make deploy-plan` until it passes |
| Offline render | `helm template ... failed with exit 1; <template>: <message>` | The chart refused a value. The message names the template and the reason |
| Preflight | `The selected context does not match target.cluster and target.server`, `Release cetirun-platform has an unfinished operation` | Find the message in [Preflight messages](#preflight-messages) |
| Public DNS and TLS, only with `publicTLS` | Messages about DNS, cert-manager, or the staging and production certificates | This stage writes the DNS label, cert-manager, and a certificate-only platform revision. Then the preflight runs again. Refer to [Public DNS and TLS](../public-ingress/#deploy-and-verify) |
| Releases | `helm upgrade cetirun-platform --namespace cetirun-system --timeout 660s failed with exit 1` | Examine the release, as [Release and workload failures](#release-and-workload-failures) shows |
| Workload readiness | `kubectl rollout status ... failed with exit 1`, `cetirun-portal is missing the required Istio sidecar` | Examine the Pods of the workload |
| Final admission check | `kubectl create --dry-run=server failed with exit 1` | The admission server refused a server-side dry run of a Team. Refer to [Platform components](#platform-components) |

`make deploy` has no automatic rollback. After you correct the cause, run
`make deploy` again with the same environment file.

### Preflight messages

| Message | Cause | Check |
| --- | --- | --- |
| `The selected context does not match target.cluster and target.server` | The kubeconfig context selects another cluster | `kubectl config view --minify`. Refer to [Connect to the cluster](../infrastructure/#connect-to-the-cluster) |
| `target.clusterUID does not match this cluster's kube-system UID` | The context selects another cluster, or the cluster was created again | `kubectl get namespace kube-system -o jsonpath='{.metadata.uid}'` |
| `kubectl 1.35 is outside the supported one-minor skew of Kubernetes 1.37; ...` | The kubectl client is more than one minor version from the cluster | `kubectl version` |
| `Managed Istio prerequisite is missing: ...` | Managed Istio is not enabled, or its CRDs are missing | `kubectl get crd gateways.networking.istio.io` |
| `The configured istio.ingressDeployment ... does not exist` | A name in the `istio` section has a wrong revision | [Write the environment file](../../getting-started/platform/#6-write-the-environment-file) |
| `Release <release> has an unfinished operation` | A Helm operation stopped before it finished, or another writer uses the release now | [An unfinished release operation](#an-unfinished-release-operation) |
| `Refusing to adopt <kind> <namespace>/<name> from another owner` | Another Helm release or a manual command created the object | Read the `meta.helm.sh/release-name` annotation of the object |
| `Required Secret reference is missing: <namespace>/<name>` or `Required Secret key is missing: <namespace>/<name>:<key>` | A Secret or a key that the environment file names does not exist | `kubectl get secret <name> -n <namespace>`. Do not print the values |
| `Retained Secret <namespace>/<name> is missing; refusing regeneration` or `Retained PostgreSQL data has no password Secret; refusing to generate a replacement` | A Secret that a release generated was deleted. For the second message, the database volume of the AI Gateway still exists | Restore the Secret. Refer to [Make and restore backups](../backup-restore/) |
| `openssl verify failed with exit ...`, `openssl x509 failed with exit 1`, `The ingress certificate does not cover the configured domain`, or `The ingress certificate is not yet valid` | A certificate check failed. `openssl verify` covers the admission, MCP, and Agent Identity Broker certificates, and its message does not name the certificate. The other messages are about `cetirun-tls` | Examine each certificate, as [Certificates and credentials](../certificates/) shows |
| `docker buildx imagetools inspect <image> failed with exit 1` or `helm show chart oci://<registry>/helm/<chart> --version <version> failed with exit 1` | The image or the chart is not in the registry, or the workstation cannot read the registry | Run the same command |
| A message that starts with `Preserve existing` | The environment file tries to change a protected setting of the installation | Refer to [Settings that make deploy keeps](../upgrade/#settings-that-make-deploy-keeps) |

### An unfinished release operation

`make deploy` stops if a release is in a `pending-install`, `pending-upgrade`,
or `pending-rollback` state. A release stays in such a state if a Helm client
stopped during the operation.

1. Make sure that no other `make deploy` or Helm command writes to the release.
2. Read the history of the release, for example:

   ```bash
   helm history cetirun-platform -n cetirun-system
   ```

3. Decide on the recovery with the owner of the environment. A recovery writes
   to the cluster, for example a rollback to the last `deployed` revision.
   Refer to [Upgrade CetiRun](../upgrade/#return-to-an-earlier-version) for the data that a
   rollback does not restore.

Do not delete the Helm release Secrets to force a new run.

### Release and workload failures

After the preflight, `make deploy` installs the releases in this order:

1. The CetiRun Istio release
2. The OAuth2 Proxy provider of the mesh
3. The Broker
4. The platform

On a new installation, it first installs the platform without
enforced admission, and then with it. Examine the release and the namespace of
the failed step, for example:

```bash
helm status cetirun-platform -n cetirun-system
helm history cetirun-platform -n cetirun-system
kubectl get pods -n cetirun-system
kubectl get events -n cetirun-system --field-selector type=Warning --sort-by=.lastTimestamp
```

- `<workload> is missing the required Istio sidecar`: the Pods of the Operator,
  the Portal, the Broker, or Connections MCP have no `istio-proxy` container.
  Make sure that the `istio.io/rev` label of the namespace names the installed
  revision: `kubectl get namespace <namespace> --show-labels`.
- `Broker pods are missing the Auth SDK`: the Broker Pods have no `auth-sdk`
  container. Microsoft delegated access adds this container.

## Platform components

| Symptom | Possible cause | Read-only checks |
| --- | --- | --- |
| The Portal cannot create or change Agents, Teams, Sandboxes, or Agent Policies. The error contains `failed calling webhook` | The Sandbox admission webhooks use `failurePolicy: Fail`. When the admission server is not available, the API server refuses these writes | `kubectl get validatingwebhookconfiguration cetirun-sandbox-admission` and `kubectl get deployment cetirun-sandbox-admission -n cetirun-system` |
| No user can sign in | OAuth2 Proxy runs only on the `system` node pool, and its Pods are not ready | `kubectl get pods -n cetirun-istio -o wide` and `kubectl get nodes -l agentpool=system` |
| New Agents get no status | The Operator is not ready | `kubectl get deployment cetirun-operator -n cetirun-system` |
| Agents do not get models | The AI Gateway or its database is not ready | `kubectl get deployment cetirun-ai-gateway -n cetirun-system` and `kubectl get statefulset cetirun-ai-gateway-postgresql -n cetirun-system`. Refer to [AI Gateway](../ai-gateway/) |
| Agent connections fail | The Broker or Connections MCP is not ready | `kubectl get pods -n <broker namespace>` and `kubectl get deployment cetirun-connections-mcp -n cetirun-system`. Refer to [Prepare Agent connections](../agent-identity/#troubleshooting) |

Read the logs of a platform component privately. The logs of the Portal and of
OAuth2 Proxy can contain user identities.

## Agent status

Start with the status and the events. Then examine only the layer that fails.
Replace the example values with the actual Agent name and its reported
namespace. The `alice-example` name is synthetic.

```bash
kubectl get agents.cetirun.microsoft.com alice-example
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.namespace}{"\n"}{.status.helmReleaseName}{"\n"}{.status.conditions}{"\n"}'
kubectl get statefulset,pods,pvc,service -n cetirun-alice-example
kubectl get events -n cetirun-alice-example --sort-by=.lastTimestamp
```

Resource names use `metadata.name`, not `spec.user.alias`. With the controller
and chart defaults, the namespace of this example is `cetirun-alice-example`.
Its Helm release is `openclaw-alice-example`, and its StatefulSet and Service
are both `openclaw-alice-example-cetirun`. You cannot use one of these API
names in place of another. Use the status and the actual resource lists, not
guesses.

Sources: [name and routing helpers]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [reconciler]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}).

### Read governance status in the Portal

The **My Agents** list and the **All agents** list of Portal administrators
show a governance cause next to the lifecycle phase. The Agent detail page
shows the same cause and a suggested next step on every tab. For example, an
Agent can remain `Pending` while the Portal shows **Blocked by policy** with
`PolicyDenied` and the reason from the Agent Policy.

| Portal status | Next step |
| --- | --- |
| Blocked by policy | Review the Agent configuration against the Team's Agent Policy. Read the reported reason. |
| Identity owner binding required | Ask an administrator to verify the trusted owner binding for Agent Identity. |
| Binding or identity approval required | Ask an administrator to verify the Team binding and the Agent's identity or role approvals. |
| Identity not ready | Read the reported reason. Then follow [Troubleshoot Agent Identity](../entra-agent-identity/#troubleshoot-agent-identity). |
| Platform support required | Verify platform support for the requested Sandbox controls. |
| Governance dependency unavailable | Examine the required Agent Policy, Sandbox, and platform dependencies. |
| Waiting for governance | Wait for the Operator to verify prerequisites. If the condition persists, examine the detailed conditions. |

`UnauthorizedBinding` can mean a binding or identity grant is not authorized.
It does not identify Entra approval as the only possible cause. The Portal
keeps that distinction and does not infer a specific identity failure from a
generic enforcement wait.

The summary requires an observation for the current Agent generation. Some
Agent Policy and Agent Identity conditions do not have their own observed
generation. For those conditions, the Portal checks the generation and matching
state in the corresponding Agent Policy or identity status. It omits older
observations after a configuration change and does not override deletion or
suspension.

Specific causes take precedence over a generic enforcement wait. Full reported
conditions remain available under **Status**. The summary describes the latest
reported observation; it does not approve a grant or verify live enforcement.

### Follow the first failure

| Observation | Inspect next |
| --- | --- |
| No status or repeated reconciliation errors | Operator rollout, logs, CRD version, and RBAC |
| `HelmDeployed` is false | OCI chart and version, operator registry credentials, values validation, admission or RBAC failures, and upgrade hooks |
| PVC pending or mount failure | StorageClass, Blob CSI, subnet rules, and mount events |
| Pod cannot be scheduled | RuntimeClass existence and scheduling, node taints, and total resource requests and capacity |
| Image pull failure | Actual image reference or digest, node registry permissions, and the namespace-local pull Secret |
| Secret key does not exist, or container configuration error | Gateway Secret key names, agent type, and controller enrichment. Do not print the values. |
| Pod stays in `Init`, and the `istio-validation` init container fails | The managed Istio traffic redirection of the cluster is not `InitContainers`. Refer to [Cluster requirements](../../getting-started/platform/#cluster-requirements) |
| Init container fails | `init-home-data` permissions and mounts, OpenClaw `doctor`, or npm `init-skills` registry and package access |
| `HomeSeedStateUnknown` or `HomeSeedImageUnconfirmed` | The HOME and initialization records that exist, and the source image of the interrupted copy. Do not clear the PVC or delete records to retry. |
| Native configuration apply fails | JSON/YAML or environment-file syntax, destination permissions and symlinks. The initializer keeps the files that failed. |
| `PodReady` is false | StatefulSet rollout generation, ready and updated replicas, probes, and desktop dependencies |
| `IngressReady` is false | Instance VirtualService and the shared ingress gateway |
| Login loop or 403 | OAuth callback and TLS, groups, sidecar injection, and ownership |
| Portal returns HTTP 403 `invalid_identity` | The user name. The part before `@` must give a valid alias. The Portal does not replace characters. Refer to [User names](../../getting-started/platform/#user-names) |
| Agent opens but model fails | AI Gateway conditions, Portal sign-in, selected model entitlement, DNS, and egress. Readiness does not test inference. Refer to [Troubleshoot model requests](../ai-gateway/#troubleshoot-model-requests). |

The controller can mark the Agent `Ready` even when the
VirtualService reconciliation failed. Read `IngressReady`. Then do a separate
check to make sure that access works. Also, `HelmDeployed=True` means that the
chart submission succeeded, but it does not wait for pod readiness. A suspended
Agent intentionally has zero desired replicas and no VirtualService. Before
you decide that absent pods are a failure, examine `spec.suspended` and the
phase.

After a workload apply, readiness uses a current API read of the StatefulSet.
It does not use a pre-apply informer snapshot, which can be stale. The
generation, replica, restart, and migration checks that already exist use that
current object. If the current read fails, the failure is reported. A cached
Ready status is not used instead.

The Custom terminal also waits until its one current Deployment replica is
updated, ready, and available, and until no older surge replica is left. If
only the previous terminal Pod is available, the rollout is not complete.
These observations do not replace model/tool or provider acceptance.

If the main workload is ready but the Custom terminal keeps returning HTTP
503, inspect the terminal container logs. A listening ttyd socket alone does
not establish readiness: the terminal must also read and authorize its target
and successfully probe a supported shell through Kubernetes exec.

The terminal reports fixed startup stages and error categories:

| Stage | What to investigate |
| --- | --- |
| `agent-read` | Kubernetes API connectivity and the terminal ServiceAccount's Agent read permission |
| `target-resolve` | Current Agent authorization, target StatefulSet/Pod reads, ownership, and main-container readiness |
| `shell-probe` | Exec transport, exec RBAC/admission, and the supported shells in the target image |
| `target-recheck` | API reads or target replacement immediately after shell probing |
| `socket-ready` | Availability of ttyd's private Unix socket after the shell dependency succeeds |

Categories include `timeout`, `connection-failed`, `tls-failure`,
`authorization-denied`, `exec-upgrade-failed`, `target-waiting`,
`target-changed`, `no-shell`, and `socket-unavailable`. An `unknown` category
means the error could not be safely classified. `authorization-denied` does
not distinguish RBAC from admission or identify a particular admission rule.
If both shell probing and the subsequent target check fail, `recheck_category`
preserves the second failure alongside the shell failure.

The first observation is logged immediately. Subsequent nonfatal observations,
including recovery (`Terminal ready`), share a 30-second rate limit. Repeated
failures report cumulative `failed_attempts` and `elapsed_seconds` since the
first failure; `state_changes` counts transitions since the previous log.
When a recovery is logged, the failure counters reset. A fatal failure is
logged before exit even within the rate limit. Health requests do not generate
these logs. Diagnostics contain fixed categories and counters, not raw API
errors, credentials, request URLs, or target identities.

Use the stage to narrow an authorized investigation. These diagnostics do not
establish why a particular installation remains unready and do not relax its
authorization, network policy, or readiness requirements.

The `ValidatingAdmissionPolicy` API can be absent, or an admission-policy write
can be forbidden. In both cases, examine the default `execRestriction`
prerequisites and the operator ClusterRole. The chart does not have a condition
that omits that policy for older clusters. Do not change the cluster access
policy only to make a render install.

For the metrics and the optional alerts about the reported Sandbox status,
refer to [Monitoring and logs](../observability/).

## Routing

```bash
kubectl get virtualservice cetirun-alice-example -n cetirun-alice-example
kubectl get gateway.networking.istio.io cetirun-gateway -n aks-istio-ingress
kubectl get pods -n cetirun-system
kubectl get pods -n cetirun-istio
```

These commands use the umbrella's `cetirun-system` namespace. For a standalone
portal installation, examine its configured namespace (`cetirun-portal` by
default).

The shared ingress gateway namespace and the VirtualService namespace of each
instance are different. The internal ownership service of the portal is part
of the access path. Do not disable OAuth2 Proxy, ownership validation, or
network policy as a diagnostic shortcut.

For login loops, make sure that the Entra redirect URI matches the rendered
`https://<domain>/oauth2/callback`, not an OAuth subdomain. Also examine these
items:

- The `cetirun-tls` coverage and expiry, through the approved certificate
  workflow
- The federated trust
- The allowed groups
- The `oauth2-proxy` extension provider in the shared ConfigMap of the active
  Istio revision

A Helm render cannot validate those dependencies.

For ownership 403 errors, examine the Agent owner and the Portal service
address. Also examine the actual Portal sidecar injection and the
NetworkPolicy from the ingress gateway to the Portal.

With `make deploy`, the ownership call to port 8081 uses mesh mTLS. In this
case, also examine the `portalAuth.mtls.serverIdentity` value of the Istio
release. A standalone Portal chart can keep port 8081 without mTLS. Refer to
[Authentication and ownership](../istio/#authentication-and-ownership). The
alias of a Portal administrator is not an exception to the ownership check of
an Agent. For the separate authentication and ownership exemptions, refer to
[Istio operations](../istio/).

## Agent services

| Interface | Default |
| --- | --- |
| OpenClaw gateway | `18789` |
| Hermes API / Webapp | `8642` / `9120` |
| Desktop noVNC / Chrome debugging | `6080` / `18800` |
| ttyd terminal | `7681` |

By default, the chart enables the desktop, ttyd, and session logging. Keep the
debug and terminal endpoints behind the intended authenticated path.

OpenClaw uses `/startupz`, `/healthz`, and `/readyz` for startup, liveness, and
readiness on its gateway port. Hermes uses exec probes in its container. Its
startup and liveness probes request `/health` on the API port. Its readiness
probe also checks the Webapp status and the built Webapp assets. Examine the actual StatefulSet,
because overrides and the way that sidecars handle probes can affect the
effective pod configuration.
Sources: [OpenClaw probes]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes probes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}).

Examine only the logs of the container that fails, and examine them privately.
`logger` prints the conversations of the Agent, not diagnostic messages. The
default admission policy denies `kubectl exec` in the instance namespace,
except for a fixed service account. Do not propose exec or port-forward as a
way around the access controls of the deployment. When the portal terminal is
enabled and healthy, it is the intended authenticated user path.

### Hermes tools stop at 420 seconds

The pinned Hermes release has an outer tool deadline, in addition to the
timeout that the terminal tool requests. A larger tool request alone cannot
make an eight-minute foreground command fit in that default deadline. Use a
deliberately bounded per-command override, or the native timeout configuration
that already exists. The
[Hermes long-task guidance]({{< repo "app/cetirun-chart/agents/hermes/README.md" >}})
describes both. Keep the normal approval controls and a finite overall run
budget. An output file that is written later, a detached child, or a
successful CLI exit does not alone show a successful foreground-tool result.

## Before changing lifecycle state

A restart, a Helm upgrade or rollback, a suspension, or a deletion changes the
running environment. Authorization is necessary for each of these changes.
None of them belongs in the read-only checks above. For deliberate changes, use
[Administer Agents](../agent-lifecycle/).

If immutable fields are different, the agent chart pre-upgrade/pre-rollback hook
can recreate a StatefulSet. The hook does not migrate PVC data. Deletion of the
CR also deletes its namespace. As a result, a StatefulSet `Retain` value is not
sufficient protection. Do not delete PVCs or remove finalizers to hide a failed
reconciliation.

## External integrations

- [Copilot catalog](../../guides/models/#when-discovery-fails)
- [Tailscale](../../guides/tailscale/#if-it-does-not-connect)

## Share a useful report

Include the source revision, agent type, chart version, expected behavior,
redacted condition reasons, and a minimal reproduction. Do not include full
session logs, credentials, cloud IDs, user prompts, or persistent browser
state. For suspected vulnerabilities, use the repository’s
[instructions for security reports]({{< repo "SECURITY.md" >}}).
