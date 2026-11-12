---
title: "Policy-bound Teams"
description: "Prepare a cluster so that the Agents of a Team with an Agent Policy start in their Sandbox."
weight: 15
---

A Team that is created in the Portal has an Agent Policy and one or more
Sandboxes. Its Agents run in a Sandbox. A Kata Sandbox runs each Agent in its
own Kata virtual machine. A Container Sandbox runs ordinary containers that
share the node kernel. The Policy's network rules are compiled into Cilium
network policies, and the Sandbox admission webhook examines the writes to
these resources. `make deploy` installs and enforces Sandbox admission, but it
keeps Sandbox execution disabled. Until you
enable it, the Portal refuses to create an Agent for such a Team. An Agent that
is created for such a Team with `kubectl` is held and never starts.

> **Warning:** An Agent Policy is not an access boundary between users. Every
> signed-in Portal user can select any Team, or no Team, for a new Agent. An
> Agent Policy applies only to the Agents of a Team that has one. Put in an
> Agent Policy only the access that every Portal user can have.

A standard Agent does not select a policy-bound Team or a Sandbox. It can use
every shared model. When the deployment offers GitHub Copilot, the Agent can
also use it. A standard Agent has no request limits. Its Gateway key starts with the operator's
`aiGateway.defaultBudget`, 10000 USD a month by default. To change this budget,
set `platform.values.cetirun-operator.aiGateway.defaultBudget` before users
create Agents.

The steps on this page enable Sandbox execution for an installation that
`make deploy` makes, as in [Install CetiRun](../../getting-started/platform/),
in the release namespace `cetirun-system`. Do steps 1 to 3 before the first
`make deploy`, because it must have the admission certificate. Do the other
steps after the first run:

1. [Examine the cluster](#the-cluster).
2. [Create a HOME StorageClass](#home-storage).
3. [Issue the admission certificate and select the administrators](#the-admission-certificate).
4. [Register the platform](#the-platform-registration).
5. [Protect the selected peers](#peer-labels).
6. [Enable Sandbox execution](#enable-sandbox-execution).
7. Optional: [Enable Agent connections for Sandbox Agents](../agent-identity/#enable-connections-for-sandbox-agents).
8. [Test the setup with a Sandbox, a Policy, and a Team](#check-the-setup).

Then, for a Team whose Agents use a smart-routing model, follow
[Smart routing](../smart-routing/). To give the Agents of a Team approved
skills from a Git repository, follow [Skill Marketplace](../skill-marketplace/).
For the other Team settings, follow [Configure a team](../../guides/teams/).
For the Admin pages of the Portal, refer to
[Portal administration](../portal-administration/).

## The cluster

- **Kubernetes 1.37 or later.** `make deploy` and the operator chart refuse an
  older cluster. The [cluster template](../infrastructure/) defaults to `1.37`,
  and its deployment helper rejects older overrides before any Azure writes.
  Select a stable 1.37 or later version that your region offers.
- **Kata.** Agent node pools with `workloadRuntime: KataVmIsolation` supply the
  `kata-vm-isolation` RuntimeClass. The two agent pools of the template have
  this setting.
- **Cilium.** Azure CNI powered by Cilium, which is the networking of the
  template. The operator writes a CiliumNetworkPolicy for each Agent.
- **Two nodes for the admission server.** Its two replicas run on different
  nodes. Creates and updates of Agents, Teams, Sandboxes, and Agent Policies
  wait for one of them, and so do deletions of Teams, Sandboxes, and Agent
  Policies. With the [CetiAdmin](../cetiadmin/) webhooks, which the operator
  chart installs by default, status updates and the deletion of Agents also
  wait. So do writes to Pods and other workloads in Agent namespaces. The
  webhooks stay after you disable CetiAdmin on an installation that had it.
- **ReadWriteOncePod storage.** Each Agent's HOME is a volume that only its Pod
  can mount. Azure Disk supports it.

## HOME storage

Create a StorageClass for the Agents' HOME volumes, for example:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sandbox-home
provisioner: disk.csi.azure.com
parameters:
  skuName: Premium_LRS
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

A Sandbox names this class. The platform registration below pins it.

With `reclaimPolicy: Delete`, the HOME disk of an Agent is deleted together
with the Agent. To recover HOME after the deletion of an Agent, use `Retain`
instead. Then plan the cleanup of the retained disks, because they continue to
use storage.

## The admission certificate

The admission server serves TLS under its Service name,
`cetirun-sandbox-admission.cetirun-system.svc`. The chart does not issue a
certificate. Create a CA, a server certificate for that name, and a TLS Secret.
For example, use OpenSSL. The commands write the private keys to a new private
directory outside the repository. They refuse a directory that exists:

```bash
umask 077
ADMISSION_TLS_DIR=/absolute/private/admission-tls
mkdir -m 700 "$ADMISSION_TLS_DIR" || exit 1
SERVICE=cetirun-sandbox-admission.cetirun-system.svc
openssl req -x509 -newkey rsa:2048 -nodes -days 1825 \
  -subj "/CN=cetirun-sandbox-admission-ca" \
  -keyout "$ADMISSION_TLS_DIR/ca.key" -out "$ADMISSION_TLS_DIR/ca.crt"
openssl req -newkey rsa:2048 -nodes -subj "/CN=${SERVICE}" \
  -keyout "$ADMISSION_TLS_DIR/tls.key" -out "$ADMISSION_TLS_DIR/tls.csr"
printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "${SERVICE}" \
  > "$ADMISSION_TLS_DIR/tls.ext"
openssl x509 -req -in "$ADMISSION_TLS_DIR/tls.csr" \
  -CA "$ADMISSION_TLS_DIR/ca.crt" -CAkey "$ADMISSION_TLS_DIR/ca.key" \
  -CAcreateserial -days 365 -extfile "$ADMISSION_TLS_DIR/tls.ext" \
  -out "$ADMISSION_TLS_DIR/tls.crt"
base64 < "$ADMISSION_TLS_DIR/ca.crt" | tr -d '\n' > "$ADMISSION_TLS_DIR/ca-bundle.txt"
```

Then create the namespace if it does not exist, and the TLS Secret:

```bash
kubectl get namespace cetirun-system >/dev/null 2>&1 ||
  kubectl create namespace cetirun-system
kubectl create secret tls cetirun-sandbox-admission-tls -n cetirun-system \
  --cert="$ADMISSION_TLS_DIR/tls.crt" --key="$ADMISSION_TLS_DIR/tls.key"
```

`ca-bundle.txt` holds the CA bundle on one line. Keep `ca.key` outside the
cluster. Renew the certificate before it expires. An expired certificate makes
the admission server unready. Because `make deploy` enforces admission, the
webhook then refuses every write that it examines.

The example CA is valid for five years, and the server certificate for one
year. Renew the server certificate each year, and replace the CA before it
expires. Refer to [Renew the admission certificate](#renew-the-admission-certificate).

In the environment file of `make deploy`, name the Secret and the CA bundle.
Also name the administrators:

```yaml
platform:
  values:
    cetirun-operator:
      sandboxAdmission:
        tlsSecretName: cetirun-sandbox-admission-tls
        caBundle: <the contents of ca-bundle.txt>
        # Who can write policy-bound configuration with kubectl, by the names
        # that the cluster's authentication gives them. The Portal's service
        # account is added for you. Blanket groups such as
        # system:authenticated are refused.
        adminGroups:
          - "<admin-group-object-id>"  # an Entra ID group's object ID
```

List at least one administrator in `adminUsers` or `adminGroups`. These
administrators can write policy-bound configuration with `kubectl`. They can
also reauthorize an Agent. The Portal has no page for this. Select them before
the first run, because a later `make deploy` stops if they change.

To see the user name and the groups that the cluster receives for you, run
`kubectl auth whoami` with the credentials of an administrator. With the local
admin credentials of AKS, the group is `system:masters`. Refer to
[Connect to the cluster](../infrastructure/#connect-to-the-cluster).

On a new installation, `make deploy` starts the admission server without
enforcement. After the server is ready, it enforces admission. After the run,
make sure that both replicas are ready and that they run on different nodes:

```bash
kubectl rollout status deployment/cetirun-sandbox-admission -n cetirun-system
kubectl get pods -n cetirun-system -l app.kubernetes.io/name=cetirun-sandbox-admission -o wide
```

If you install the platform chart directly with Helm, install admission
without enforcement first. Enforce it in a later upgrade, after the webhook
answers. Refer to the operator README's
[availability and bootstrap]({{< repo "app/cetirun-operator/README.md" >}}#availability-and-bootstrap).

### Renew the admission certificate

`make deploy` verifies the server certificate in the TLS Secret against
`caBundle`. The admission server reads the certificate again for each new
connection, so a restart is not necessary. To replace the CA, do these steps
in this order:

1. Put the new CA and the old CA together in one PEM file. Encode it on one
   line, as for `ca-bundle.txt`.
2. Set `caBundle` to the encoded file, and run `make deploy`.
3. Replace the TLS Secret with a server certificate that the new CA issued:

   ```bash
   kubectl create secret tls cetirun-sandbox-admission-tls -n cetirun-system \
     --cert="$ADMISSION_TLS_DIR/tls.crt" --key="$ADMISSION_TLS_DIR/tls.key" \
     --dry-run=client -o yaml | kubectl replace -f -
   ```

4. Wait until each admission replica serves the new certificate. The kubelet
   updates the mounted Secret after a delay.
5. Set `caBundle` to the new CA only, and run `make deploy`.

To renew only the server certificate, do step 3 with a certificate from the
same CA. The steps above are not verified on a cluster. Refer to the operator
README's
[availability and bootstrap]({{< repo "app/cetirun-operator/README.md" >}}#availability-and-bootstrap).

## The platform registration

Write the registration after the first `make deploy`, because it uses the
cluster IPs of the DNS and AI Gateway Services.

The operator and the admission webhook read what the cluster supplies from a
ConfigMap that you write. This is `platform.json` in `sandbox-platform`, in
the operator's namespace. They accept a Sandbox only with the RuntimeClass and
StorageClass that are pinned here. They build the network policy of each Agent
from the peers here: DNS, the AI Gateway, and the ingress gateway. Unknown
fields are refused. Fields that a Sandbox must have and that are left out are
also refused.

```json
{
  "version": "v1",
  "registrations": {
    "kata": {
      "Available": true,
      "Capabilities": {
        "profile": "native-single-start",
        "runtimeClassName": "kata-vm-isolation",
        "revision": "kata-1",
        "hostResourceLimits": true,
        "perContainerResourceLimits": false,
        "requirePerContainerResourceLimits": false,
        "readOnlyRootFilesystem": true,
        "homeAccessModes": ["ReadWriteOncePod"],
        "ipFamilies": ["IPv4"],
        "protocols": ["TCP", "UDP"],
        "guestExternalNetwork": true,
        "denyPrecedence": true,
        "nodeTraffic": true,
        "dnsIPRestrictions": true,
        "blockedPorts": false,
        "blockedFQDNs": false,
        "authenticatedIngress": true,
        "disabledIngress": false,
        "mutualTLS": false,
        "kubernetesAccess": false,
        "entraID": false,
        "enforcementObservation": false,
        "restrictiveFailure": false
      }
    }
  },
  "platforms": {
    "kata": {
      "adapter": {"name": "cilium", "revision": "<CILIUM_VERSION>", "qualified": false, "available": true},
      "topology": {
        "revision": "topology-1",
        "managedPeersComplete": true,
        "dnsResolverIPs": ["<KUBE_DNS_IP>"],
        "dns": [
          {"name": "coredns", "namespace": "kube-system", "uid": "coredns",
           "ips": ["<KUBE_DNS_IP>"],
           "podSelector": {"k8s-app": "kube-dns"}}
        ],
        "gateway": [
          {"peer": {"name": "cetirun-ai-gateway", "namespace": "cetirun-system", "uid": "ai-gateway",
                    "ips": ["<GATEWAY_SERVICE_IP>"],
                    "podSelector": {"app.kubernetes.io/name": "cetirun-ai-gateway",
                                    "app.kubernetes.io/instance": "<PLATFORM_RELEASE>",
                                    "app.kubernetes.io/component": "gateway",
                                    "sandbox.cetirun.microsoft.com/peer": "ai-gateway"}},
           "ports": [{"port": 4000, "protocol": "TCP"}]}
        ],
        "ingress": [
          {"peer": {"name": "aks-istio-ingressgateway-external", "namespace": "aks-istio-ingress",
                    "uid": "<INGRESS_DEPLOYMENT_UID>",
                    "podSelector": {"istio": "aks-istio-ingressgateway-external"}},
           "trustedProxyCIDRs": ["10.244.0.0/16"],
           "ports": [{"port": 18789, "protocol": "TCP"}, {"port": 7681, "protocol": "TCP"},
                     {"port": 8642, "protocol": "TCP"}, {"port": 9120, "protocol": "TCP"}]}
        ],
        "podCIDRs": ["10.244.0.0/16"],
        "serviceCIDRs": ["172.16.0.0/16"],
        "nodeCIDRs": ["10.224.0.0/12"],
        "managementCIDRs": ["<API_SERVER_IP>/32", "168.63.129.16/32"],
        "metadataCIDRs": ["169.254.169.254/32"]
      }
    }
  },
  "runtimeClasses": {
    "kata-vm-isolation": {"UID": "<RUNTIMECLASS_UID>", "Handler": "<RUNTIMECLASS_HANDLER>"}
  },
  "storageClasses": {
    "sandbox-home": {"UID": "<STORAGECLASS_UID>", "Provisioner": "disk.csi.azure.com"}
  }
}
```

Replace the placeholders with values from the cluster:

| Placeholder | Command |
| --- | --- |
| `<CILIUM_VERSION>` | `kubectl get ds cilium -n kube-system -o jsonpath='{.spec.template.spec.containers[0].image}'`, the image tag |
| `<KUBE_DNS_IP>` | `kubectl get svc kube-dns -n kube-system -o jsonpath='{.spec.clusterIP}'` |
| `<GATEWAY_SERVICE_IP>` | `kubectl get svc cetirun-ai-gateway -n cetirun-system -o jsonpath='{.spec.clusterIP}'` |
| `<PLATFORM_RELEASE>` | `kubectl get svc cetirun-ai-gateway -n cetirun-system -o jsonpath='{.spec.selector}'`, the value of `app.kubernetes.io/instance` |
| `<INGRESS_DEPLOYMENT_UID>` | `kubectl get deploy -n aks-istio-ingress -l app=aks-istio-ingressgateway-external -o jsonpath='{.items[0].metadata.uid}'` |
| `<API_SERVER_IP>` | `kubectl get endpointslices -n default -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[*].endpoints[*].addresses[*]}'`, one entry each |
| `<RUNTIMECLASS_UID>`, `<RUNTIMECLASS_HANDLER>` | `kubectl get runtimeclass kata-vm-isolation -o jsonpath='{.metadata.uid} {.handler}'` |
| `<STORAGECLASS_UID>` | `kubectl get storageclass sandbox-home -o jsonpath='{.metadata.uid}'`. `Provisioner` is its `provisioner` |

The CIDRs are the cluster template's defaults: Pods `10.244.0.0/16`, Services
`172.16.0.0/16`, and the nodes' AKS-managed virtual network `10.224.0.0/12`.
If your cluster has different ranges, use its own ranges. `trustedProxyCIDRs`
must be within `podCIDRs`. The `uid` of the DNS and Gateway peers is a name
that you select and keep stable. Their `podSelector` matches labels that the
workload controllers already put on Pods. Use the actual Service selectors if
your installation changes these labels. Do not list backend Pod IPs in these
selector-bound entries. The
`revision` fields are labels that you change each time that you change what
they describe.

Sandbox Pods use only the resolvers in `dnsResolverIPs`, without search
domains. A short Service name such as `<service>` or `<service>.<namespace>`
does not resolve in an Agent. Use the full name, for example
`<service>.<namespace>.svc.cluster.local`.

The ingress entry selects the Pods of the AKS-managed ingress gateway by their
own label. As a result, you do not have to change this entry after a gateway
rollout. The ports are OpenClaw's UI (`18789`), the terminal (`7681`), and
Hermes' API and Webapp (`8642`, `9120`). For its conditions, see
[rollout-stable managed ingress]({{< repo "examples/native-sandbox/README.md" >}}#rollout-stable-managed-ingress).

Create the ConfigMap. Protect it as cluster configuration. Anyone who can
write it decides what Agents can reach.

```bash
kubectl create configmap sandbox-platform -n cetirun-system --from-file=platform.json
```

To change the registration later, replace the ConfigMap. Change the
`revision` fields that describe the change. A change to the registration can
restart or stop the Sandbox Agents that use it. If configuration coordination
is enabled, suspend these Agents before the change. Refer to the operator
README's
[coordinated configuration updates]({{< repo "app/cetirun-operator/README.md" >}}#coordinated-configuration-updates).

```bash
kubectl create configmap sandbox-platform -n cetirun-system --from-file=platform.json \
  --dry-run=client -o yaml | kubectl replace -f -
```

`"profile": "native-single-start"` is the name of the registration profile of
Sandbox execution in this release. Agents of this profile restart, suspend,
and resume normally. The profile is different from the *single-start
lifetime* of the Sandbox Agents that an earlier version of Sandbox execution
started. These Agents reject restart, suspension, and migration.

### Capabilities of the registration

> **Warning:** Each `true` capability is your statement that the cluster
> enforces that control. The operator and the admission webhook do not test
> it. They accept a Policy that depends on a declared capability. Set a
> capability to `true` only after you validate the control on the target
> cluster.

The example values describe the cluster template: AKS with Kata node pools
and Azure CNI powered by Cilium. `"qualified": false` in `adapter` means that
no CetiRun test qualified this adapter for your cluster. If your CNI or Kata
configuration is different, validate each capability again. The
[Sandbox platform probes](../../reference/scripts/#sandbox-platform-probes)
examine the RuntimeClass, storage, and Cilium of a cluster, and its network
denials.

| Capability | Your statement |
| --- | --- |
| `hostResourceLimits` | The runtime enforces the CPU and memory limits of the Sandbox on the host. Necessary |
| `perContainerResourceLimits`, `requirePerContainerResourceLimits` | The runtime enforces the limits of each container. A Container Sandbox needs `perContainerResourceLimits: true`. A Kata registration must set both to `false` |
| `readOnlyRootFilesystem` | The runtime supports a read-only root filesystem. Necessary for a Policy that requests it |
| `homeAccessModes` | The HOME access modes that the storage supports: `ReadWriteOncePod`, `ReadWriteOnce`, or both |
| `ipFamilies`, `protocols` | The IP families and the protocols that the network policies control |
| `guestExternalNetwork` | The platform enforces the network controls outside the workload, where code in the Agent cannot change them. Necessary |
| `denyPrecedence` | A deny rule wins over an allow rule. Necessary |
| `nodeTraffic` | The network policy controls the traffic to the nodes. Necessary |
| `dnsIPRestrictions` | DNS traffic goes only to the registered DNS peers. Necessary |
| `blockedPorts`, `blockedFQDNs` | The network policy can block ports, or host names through observed SNI |
| `authenticatedIngress`, `disabledIngress`, `mutualTLS` | The ingress modes that the platform supports. `mutualTLS` also needs a registered endpoint. Refer to [Managed Agent ingress mTLS](../../guides/agent-ingress-mtls/) |
| `kubernetesAccess`, `entraID` | The platform supports Kubernetes API grants or Entra workload identity for Agents |
| `enforcementObservation`, `restrictiveFailure` | Must be `false` for this profile |

For the rules of each field, refer to the operator README's
[capability registration]({{< repo "app/cetirun-operator/README.md" >}}#capability-registration-and-retained-single-start-records)
and the
[native Sandbox example]({{< repo "examples/native-sandbox/README.md" >}}).

## Protect the selected peers {#peer-labels}

The DNS and Gateway entries above select Pods by the exact namespace and all
registered Pod labels. CoreDNS and the AI Gateway controllers keep those labels
on replacement Pods. Gateway rollouts, CoreDNS scaling, and node changes do not
require manual `sandbox.cetirun.microsoft.com/peer-uid` labels, new backend IP
registrations, or an Agent restart for network policy. Only the registered
ports are allowed. Other Pods and ports remain denied.

The Gateway entry must select the protected label
`sandbox.cetirun.microsoft.com/peer: ai-gateway`. The operator refuses a
selector-bound Gateway entry without it. The AI Gateway chart sets this label
on the Gateway Pods only. With native execution on, the operator chart installs
the ValidatingAdmissionPolicy `cetirun-sandbox-protected-peer`. In the
namespaces of `cetirun-operator.aiGateway.peerNamespaces` (by default the
operator namespace), only the ReplicaSet controller can create Pods with this
label, and no update can add, change or remove it.

Protect these namespaces. Tenants must not be able to create, replace, or
relabel Pods that match these selectors, or change the Gateway Deployment and
its ReplicaSets. The registration does not discover or independently verify
the Service-to-Pod association. Review the actual Service selectors and Pod
templates before approving it. For each ServiceAccount, user and group with
access to the namespace, examine the permissions:

```bash
kubectl auth can-i create pods -n cetirun-system --as system:serviceaccount:<namespace>:<name>
kubectl auth can-i patch pods -n cetirun-system --as system:serviceaccount:<namespace>:<name>
kubectl auth can-i patch deployments.apps -n cetirun-system --as system:serviceaccount:<namespace>:<name>
kubectl auth can-i create replicasets.apps -n cetirun-system --as system:serviceaccount:<namespace>:<name>
```

Only platform administrators may get `yes`. The DNS entry selects CoreDNS
Pods, which the cluster manages and CetiRun cannot label. The DNS selector
relies on cluster RBAC alone. Run the same checks with `-n kube-system`.

An installation that already uses a selector-bound Gateway entry without the
protected label must upgrade the AI Gateway chart first. Its Pods restart one
time with the label. Then add the label to the Gateway entry when you upgrade
the operator. Until then, Agents that use the entry stay held.
When the registration is not valid, the AgentPolicy `Accepted` condition and
the Agent `PolicyResolved` condition show the reason.
This mode requires the native Cilium profile and stable Service IPs within
`serviceCIDRs`, not backend Pod IPs. Service address, selector, namespace, or
peer identity changes still require a coordinated registration update. A
blocked CIDR that overlaps `podCIDRs` is rejected with this mode because its
effect on changing selected backends cannot be established. Explicit Service
IP and port denies still take precedence.

If your registration lists Pod IPs instead of selectors, keep those IPs and
their `sandbox.cetirun.microsoft.com/peer-uid` labels current until you change
the registration. Do not remove those labels while an Agent still uses such a
registration. Before you change an installation that exists to selectors,
upgrade the operator and the admission server. See
[rollout-stable DNS and AI Gateway]({{< repo "examples/native-sandbox/README.md" >}}#rollout-stable-dns-and-ai-gateway)
for the contract and validation limits.

## Enable Sandbox execution

Add these values to the `platform.values` of the environment file, and keep
the values that are already there. Then run `make deploy` again. Keep these
values in every later run. `make deploy` stops if a run removes the operator
values.

```yaml
platform:
  values:
    cetirun-operator:
      sandboxAdmission:
        podsEnabled: true
      sandboxSources:
        enabled: true
        nativeExecution: true
        bootstrapOwnedPrerequisites: true
        platformConfigMapName: sandbox-platform
    cetirun-portal:
      # The Portal creates Sandbox Agents with the image and chart that the
      # operator runs. Without this value, the Portal needs
      # sandboxRuntimeProfiles for each harness.
      sandboxRuntimeDefaultsFromController: true
```

Before it writes, `make deploy` makes sure that the `sandbox-platform`
ConfigMap exists. A Sandbox Agent runs the operator's runtime chart and the
OpenClaw or Hermes image of the same release.

## Check the setup

In the Portal, under **Admin**:

1. **Sandboxes → New Sandbox**: select Kata, the `sandbox-home` StorageClass,
   and the default sizes. The form suggests a helper memory limit of `2Gi`.
   This limit leaves room for OpenClaw's initialization.
2. **Agent policies → New policy**: select the harnesses that Agents can run and
   the shared models that they can use. Also set their monthly Gateway budget.
   This budget is necessary if they can use a model. To let Agents use their
   owners' GitHub Copilot, also enable **Allow GitHub Copilot**. Use only the
   settings that the registration supports. Refer to
   [Policy settings and registration capabilities](#policy-settings-and-registration-capabilities).
3. **Teams → New Team**: select the Policy. Select the Sandbox as the allowed
   and default Sandbox.

Make sure that the Sandbox reports ready and that the Policy reports accepted:

```bash
kubectl get sandboxes.cetirun.microsoft.com,agentpolicies.cetirun.microsoft.com,teams.cetirun.microsoft.com
```

If a Sandbox is not ready, it names the cause in its conditions. An example is
a StorageClass that the registration does not pin (`UnapprovedStorage`).

The `Accepted` condition of a Policy gives only a general reason, for example
`UnsupportedControl`. It also does not assess the Policy against the Sandboxes
of a Team. To find the setting that the registration does not support, open
**Admin → Teams**, and select **Details** for the Team. The compatibility
report names the unsupported control for each Sandbox. With `kubectl`, read
`status.compatibility` of the Team.

Members can then create Agents for the Team, as in
[Your first instance](../../getting-started/first-instance/). After the first
Agent starts, make sure that the Policy applies to it:

```bash
kubectl get agent <agent> -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
```

`SandboxReady` and `PolicyApplied` must be `True`. For the status in the
Portal, refer to
[Read governance status in the Portal](../troubleshooting/#read-governance-status-in-the-portal).

### Policy settings and registration capabilities

The Policy editor offers controls that the example registration does not
support. A Team whose Policy uses such a control becomes incompatible. Before
you use a control, add its capability to the registration, and validate the
control on the cluster. Refer to
[Capabilities of the registration](#capabilities-of-the-registration).

| Policy setting | Capability of the registration | In the example registration |
| --- | --- | --- |
| **Blocked ports** | `blockedPorts` | No |
| A UDP or SCTP port in **Blocked ports** or in an internal network rule | `protocols` lists the protocol | TCP and UDP |
| An IPv6 range in **Blocked CIDRs** or in an internal network rule | `ipFamilies` lists `IPv6` | IPv4 only |
| **Ingress mode: Authenticated** | `authenticatedIngress` | Yes |
| **Ingress mode: Disabled** | `disabledIngress` | No |
| **Use a read-only root filesystem** | `readOnlyRootFilesystem` | Yes |
| **Entra workload identity** | `entraID` | No |
| Kubernetes API access, only with `kubectl` | `kubernetesAccess` | No |
| Blocked FQDNs, only with `kubectl` | `blockedFQDNs` | No |
| Ingress mTLS required, only with `kubectl` | `mutualTLS` and a registered endpoint | No |

**Agent Identity** in the Policy needs no registration capability.

### Team operations rejected by admission

An HTTP 403 can come from the Sandbox admission webhook, even when the Portal
ServiceAccount has permission to create or update Teams. The Portal reports
these webhook refusals as `admission_rejected`, separately from
`rbac_forbidden`. Examine the selected Policy, Sandbox, and registered
topology. Then examine the Portal logs. Do not increase ClusterRole
permissions only to solve a network conflict. The response does not include
raw webhook details, and admission and RBAC enforcement do not change.

## What changes for Agents

- Sandbox Agents can reach public internet addresses by default. The compiled
  Cilium policy denies private, Pod, Service, node, management, and metadata
  networks. Registered platform peers, for example DNS and the AI Gateway, and
  the `network.allowedInternalCIDRs` grants of the Policy are the exceptions.
  `network.blockedCIDRs` and `network.blockedPorts` remove addresses and ports
  from the allowed traffic. `network.blockedFQDNs` needs a separately
  registered observed-SNI capture, and it is not complete hostname
  enforcement.
- Tailscale is not available to Sandbox Agents.
- Custom Agents and Entra projection must have the separate approved-image
  and identity setup in the
  [Custom contract](../../reference/agent-harness/#policy-governed-custom-execution).
  Agent connections and Agent Identity work only after
  [Enable Agent connections for Sandbox Agents](../agent-identity/#enable-connections-for-sandbox-agents).
- A change to the model access, budget, network, or security settings of a
  Policy restarts its running Agents one time. A change to only its request
  limits or its allowed skills does not. After such a change, a Sandbox Agent
  that an earlier version of Sandbox execution started stays stopped until it
  is recreated.
- A switch of the **Agent Identity** setting of a Policy between **Disabled**
  and **Optional** does not restart the Agents that do not request an
  identity. **Required** stops these Agents. Refer to
  [Choose the capabilities](../agent-identity/#choose-the-capabilities).
- Kubernetes `exec`, `attach`, and `port-forward` requests to the Pods of
  Sandbox Agents fail with `ConnectForbidden`, also for cluster
  administrators. Use the terminal of the Agent in the Portal.
- An Agent moves to another Team only when an administrator reauthorizes it
  with `kubectl`. Refer to
  [Move an Agent to another Team](#move-an-agent-to-another-team).
- The Team skills of these Agents can come from one Git repository, through
  the optional [Skill Marketplace](../skill-marketplace/). A later
  `make deploy` run enables it.

### Helm values of Sandbox Agents

For an Agent of a policy-bound Team, the Sandbox and the Agent Policy win. The
operator builds the Helm values of such an Agent in this order. A later step
replaces the values of an earlier step:

1. The deployment values of the operator, `cetirun.helmValues`.
2. The shared context and the skills of the Team.
3. The per-Agent overrides that the operator allows.
4. The AI Gateway configuration: the endpoint, the virtual key, the models,
   and the default model.
5. The protected values of the Sandbox and the Agent Policy, for example HOME,
   skills, resources, security context, and network policy.

The operator refuses per-Agent overrides of the values that the Sandbox or the
Policy controls. Examples are `persistence`, `resources`, `securityContext`,
`podSecurityContext`, `networkPolicy`, `replicaCount`, `ingress`, and
`serviceAccount`. For all Agents, it also refuses `aiGateway`,
`connectionsMCP`, `image`, `imagePullSecrets`, `memoryContext`, the provider
fields of `secrets`, and Pod annotations that start with `adx-mon/`. For an
Agent without a policy, the order of
[Configuration and skills](../../concepts/configuration/) applies.

## Move an Agent to another Team

An Agent of a policy-bound Team keeps its Team, Sandbox, and Agent Policy
until an admission administrator reauthorizes it. The Portal has no page for
this. Do these steps with `kubectl`, as an admission administrator. The values
`sandboxAdmission.adminUsers` and `sandboxAdmission.adminGroups` name these
administrators:

1. Suspend the Agent:

   ```bash
   kubectl patch agent <agent> --type merge -p '{"spec":{"suspended":true}}'
   ```

2. Wait until the Agent is suspended:

   ```bash
   kubectl wait agent/<agent> --for=jsonpath='{.status.phase}'=Suspended --timeout=5m
   ```

3. Select a Sandbox that the new Team allows. Read the UIDs of the Agent, the
   Team, the Sandbox, and the Agent Policy of the Team:

   ```bash
   AGENT=<agent> TEAM=<team> SANDBOX=<sandbox>
   POLICY=$(kubectl get team "$TEAM" -o jsonpath='{.spec.agentPolicyRef}')
   AGENT_UID=$(kubectl get agent "$AGENT" -o jsonpath='{.metadata.uid}')
   TEAM_UID=$(kubectl get team "$TEAM" -o jsonpath='{.metadata.uid}')
   SANDBOX_UID=$(kubectl get sandboxes.cetirun.microsoft.com "$SANDBOX" -o jsonpath='{.metadata.uid}')
   POLICY_UID=$(kubectl get agentpolicies.cetirun.microsoft.com "$POLICY" -o jsonpath='{.metadata.uid}')
   ```

4. In one update, change `spec.team` and `spec.sandboxRef`. In the same
   update, set the annotation `cetirun.microsoft.com/sandbox-reauthorize`. Its
   value has a new `requestID` and the four UIDs:

   ```bash
   REQUEST=$(jq -cn --arg id "<new-request-id>" --arg agent "$AGENT_UID" \
     --arg team "$TEAM_UID" --arg sandbox "$SANDBOX_UID" --arg policy "$POLICY_UID" \
     '{requestID: $id, binding: {agentUID: $agent, teamUID: $team, sandboxUID: $sandbox, agentPolicyUID: $policy}}')
   kubectl patch agent "$AGENT" --type merge -p "$(jq -cn --arg request "$REQUEST" \
     --arg team "$TEAM" --arg sandbox "$SANDBOX" \
     '{metadata: {annotations: {"cetirun.microsoft.com/sandbox-reauthorize": $request}},
       spec: {team: $team, sandboxRef: $sandbox}}')"
   ```

5. Resume the Agent:

   ```bash
   kubectl patch agent "$AGENT" --type merge -p '{"spec":{"suspended":false}}'
   ```

6. Make sure that `SandboxReady` and `PolicyApplied` become `True`.

Use a new `requestID` for each reauthorization, with at most 128 characters
and no spaces. The admission webhook refuses the update in these cases:

- The caller is not an admission administrator.
- The `requestID` is the same as in the previous request.
- The UIDs do not match the new Team, the Sandbox, and the Policy of that Team.
- The Team does not allow the Sandbox.

Admission keeps the earlier requests as replay state. Do not remove the
annotations `cetirun.microsoft.com/sandbox-authorization` and
`cetirun.microsoft.com/sandbox-reauthorize`. With GitOps or server-side apply,
keep these annotations out of the desired manifests. Do not copy them from an
export of the live object.

These steps are not verified on a cluster. For the contract, refer to the
operator README's
[explicit reauthorization]({{< repo "app/cetirun-operator/README.md" >}}#direct-kubernetes-writer-assignments-and-explicit-reauthorization).

## Monitor and troubleshoot

| Symptom | Cause | Action |
| --- | --- | --- |
| A Sandbox or a Policy reports `PrerequisitesUnavailable` with the status `Unknown` | `platform.json` is not valid, for example with an unknown field, a duplicate field, or a wrong type. The status does not name the field. The operator log gives only the type of error, such as `malformed, unknown or duplicate platform JSON fields` | Compare each field of `platform.json` with the example on this page. Make sure that the file is valid JSON, for example with `jq empty platform.json`. Then replace the ConfigMap |
| A Sandbox Agent does not start, and its `Ready` and `SandboxReady` conditions have the reason `SandboxHeld` | The operator holds the Agent. If `PolicyResolved` is `False`, the message ends with the message of `PolicyResolved`, for example when the current registration cannot enforce the policy. Other holds, for example a wait for a prerequisite, show only the general message | For a policy message, read the `Accepted` condition of the AgentPolicy, and correct the policy or the platform registration. For the general message, examine the other conditions of the Agent and the operator log |
| The Portal refuses a new Agent with `runtime_unavailable` | Sandbox execution is not enabled, or the Portal has no runtime profile for the agent type | Do [Enable Sandbox execution](#enable-sandbox-execution) |
| `kubectl exec`, `attach`, or `port-forward` fails with `ConnectForbidden` | Pod admission refuses interactive access to the Pods of Sandbox Agents | Use the terminal of the Agent in the Portal |
| Writes to Agents, Teams, Sandboxes, and Agent Policies fail | The admission server is not ready, for example because its certificate expired | Renew the certificate. Refer to [Renew the admission certificate](#renew-the-admission-certificate). If the CetiAdmin webhooks are installed, status updates and the deletion of Agents also fail until the server is ready. So do writes to Pods and other workloads in Agent namespaces |
| A Team operation fails with `admission_rejected` | The webhook refused the change | Refer to [Team operations rejected by admission](#team-operations-rejected-by-admission) |

Alert on these metrics of the admission Pods:

- `cetirun_sandbox_admission_certificate_not_after_seconds`: the expiry time
  of the serving certificate. Alert when it is zero, which means that the
  certificate cannot be read, and when the expiry is near.
- `cetirun_sandbox_admission_decisions_total`: the outcomes `allowed`,
  `denied`, `invalid_request`, and `dependency_error` for each handler. Alert
  on dependency errors.
- `cetirun_sandbox_admission_request_duration_seconds`: alert when the latency
  stays near `sandboxAdmission.timeoutSeconds`, 10 seconds by default.

The serving-certificate metric does not examine the `caBundle` that the API
server uses. Examine that bundle separately at each rotation.

> **Warning:** The admission TLS listener authenticates only the server, not
> the API server as its client. The metrics port of the admission Pods is
> plaintext, and it has no authentication. The chart installs no
> NetworkPolicy for the admission server. Before you restrict its network
> access, find the actual source addresses of the API server, the node health
> probes, and the metrics scrapers. A wrong restriction blocks the webhook,
> and then every write that it examines fails.

For audits, the `sandbox-policy` logger of the operator writes these events to
the operator log: `sandbox.policy.accepted`, `sandbox.policy.rejected`,
`sandbox.policy.applied`, `sandbox.enforcement.failed`, and
`sandbox.enforcement.lost`. Refer to the operator README's
[Monitoring and platform boundaries]({{< repo "app/cetirun-operator/README.md" >}}#monitoring-and-platform-boundaries).

Sources: [Sandbox capabilities]({{< repo "app/cetirun-operator/internal/sandbox/capabilities.go" >}}),
[network compiler]({{< repo "app/cetirun-operator/internal/sandboxpolicy/network.go" >}}),
[admission chart]({{< repo "app/cetirun-operator/chart/templates/sandbox-admission.yaml" >}}),
[native Sandbox example]({{< repo "examples/native-sandbox/README.md" >}}),
and the operator README's
[common Sandbox reconciliation]({{< repo "app/cetirun-operator/README.md" >}}#common-sandbox-reconciliation-explicit-opt-in).
