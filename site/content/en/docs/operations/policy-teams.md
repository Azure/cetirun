---
title: "Policy-bound Teams"
description: "Prepare a cluster so that the Agents of a Team with an Agent Policy start in their Sandbox."
weight: 15
---

A Team that is created in the Portal has an Agent Policy and one or more
Sandboxes. Its Agents run in a Sandbox, each in a Kata virtual machine. The
Policy's network rules are compiled into Cilium network policies, and the
Sandbox admission webhook examines every write. The platform chart keeps this
disabled. Until it is enabled, the Portal refuses to create an Agent for such a
Team. An Agent that is created for such a Team with `kubectl` is held and never
starts.

The steps on this page enable it for an installation that was made with
[Install CetiRun](../../getting-started/platform/), in the release namespace
`cetirun-system`:

1. [Examine the cluster](#the-cluster).
2. [Create a HOME StorageClass](#home-storage).
3. [Issue the admission certificate](#the-admission-certificate).
4. [Enable admission without enforcement](#admission-without-enforcement).
5. [Register the platform](#the-platform-registration).
6. [Label the peers](#peer-labels).
7. [Enforce admission and run Sandboxes](#enforce-admission-and-run-sandboxes).
8. [Test the setup with a Sandbox, a Policy, and a Team](#check-the-setup).

Then, for a Team whose Agents use a smart-routing model, follow
[Smart routing](../smart-routing/). For the other Team settings, follow
[Configure a team](../../guides/teams/).

## The cluster

- **Kubernetes 1.37 or later.** The operator chart refuses Sandbox admission on
  an older cluster. The default of the [cluster template](../infrastructure/)
  is `1.34`. In your parameter file, set `kubernetesVersion` to a 1.37 or later
  version that your region offers.
- **Kata.** Agent node pools with `workloadRuntime: KataVmIsolation` supply the
  `kata-vm-isolation` RuntimeClass. The two agent pools of the template have
  this setting.
- **Cilium.** Azure CNI powered by Cilium, which is the networking of the
  template. The operator writes a CiliumNetworkPolicy for each Agent.
- **Two nodes for the admission server.** Its two replicas run on different
  nodes. Every write to Agents, Teams, Sandboxes, and Agent Policies waits for
  one of them.
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

## The admission certificate

The admission server serves TLS under its Service name,
`cetirun-sandbox-admission.cetirun-system.svc`. The chart does not issue a
certificate. Create a CA, a server certificate for that name, and a TLS Secret.
For example, use OpenSSL:

```bash
SERVICE=cetirun-sandbox-admission.cetirun-system.svc
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -subj "/CN=cetirun-sandbox-admission-ca" -keyout ca.key -out ca.crt
openssl req -newkey rsa:2048 -nodes -subj "/CN=${SERVICE}" \
  -keyout tls.key -out tls.csr
printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "${SERVICE}" > tls.ext
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 365 -extfile tls.ext -out tls.crt
kubectl create secret tls cetirun-sandbox-admission-tls -n cetirun-system \
  --cert=tls.crt --key=tls.key
base64 -w0 ca.crt > ca-bundle.txt
```

`ca-bundle.txt` holds the CA bundle for the enforcement step. Keep `ca.key`
outside the cluster. Renew the certificate before it expires. An expired
certificate makes the admission server unready. After admission is enforced,
every write that it examines is then refused. The operator README describes
[how to rotate the CA]({{< repo "app/cetirun-operator/README.md" >}}#availability-and-bootstrap).

## Admission without enforcement

First, install admission without enforcement. This lets admission run and
answer before its refusals have an effect. An installation with enforcement
from the start is not supported. Add the values below to your platform values
file. Then run the deployment helper again.

```yaml
cetirun-operator:
  sandboxAdmission:
    enabled: true
    podsEnabled: true
    tlsSecretName: cetirun-sandbox-admission-tls
    # Who may write policy-bound configuration with kubectl, by the names
    # the cluster's authentication gives them. The Portal's service account
    # is added for you. Blanket groups such as system:authenticated are refused.
    adminGroups:
      - "<admin-group-object-id>"  # an Entra ID group's object ID
```

List at least one administrator in `adminUsers` or `adminGroups`. These
administrators can write policy-bound configuration with `kubectl`. They can
also reauthorize an Agent. The Portal has no page for this. Then make sure that
both replicas are ready and that they run on different nodes:

```bash
kubectl rollout status deployment/cetirun-sandbox-admission -n cetirun-system
kubectl get pods -n cetirun-system -l app.kubernetes.io/name=cetirun-sandbox-admission -o wide
```

## The platform registration

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
           "ips": ["<KUBE_DNS_IP>", "<COREDNS_POD_IP>", "<COREDNS_POD_IP>"]}
        ],
        "gateway": [
          {"peer": {"name": "cetirun-ai-gateway", "namespace": "cetirun-system", "uid": "ai-gateway",
                    "ips": ["<GATEWAY_SERVICE_IP>", "<GATEWAY_POD_IP>", "<GATEWAY_POD_IP>"]},
           "ports": [{"port": 4000, "protocol": "TCP"}]}
        ],
        "ingress": [
          {"peer": {"name": "aks-istio-ingressgateway-external", "namespace": "aks-istio-ingress",
                    "uid": "<INGRESS_DEPLOYMENT_UID>",
                    "podSelector": {"istio": "aks-istio-ingressgateway-external"}},
           "trustedProxyCIDRs": ["10.244.0.0/16"],
           "ports": [{"port": 18789, "protocol": "TCP"}, {"port": 7681, "protocol": "TCP"},
                     {"port": 8642, "protocol": "TCP"}, {"port": 9119, "protocol": "TCP"}]}
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
| `<COREDNS_POD_IP>` | `kubectl get pods -n kube-system -l k8s-app=kube-dns -o jsonpath='{.items[*].status.podIP}'` |
| `<GATEWAY_SERVICE_IP>` | `kubectl get svc cetirun-ai-gateway -n cetirun-system -o jsonpath='{.spec.clusterIP}'` |
| `<GATEWAY_POD_IP>` | `kubectl get pods -n cetirun-system -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway -o jsonpath='{.items[*].status.podIP}'` |
| `<INGRESS_DEPLOYMENT_UID>` | `kubectl get deploy -n aks-istio-ingress -l app=aks-istio-ingressgateway-external -o jsonpath='{.items[0].metadata.uid}'` |
| `<API_SERVER_IP>` | `kubectl get endpointslices -n default -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[*].endpoints[*].addresses[*]}'`, one entry each |
| `<RUNTIMECLASS_UID>`, `<RUNTIMECLASS_HANDLER>` | `kubectl get runtimeclass kata-vm-isolation -o jsonpath='{.metadata.uid} {.handler}'` |
| `<STORAGECLASS_UID>` | `kubectl get storageclass sandbox-home -o jsonpath='{.metadata.uid}'`. `Provisioner` is its `provisioner` |

The CIDRs are the cluster template's defaults: Pods `10.244.0.0/16`, Services
`172.16.0.0/16`, and the nodes' AKS-managed virtual network `10.224.0.0/12`.
If your cluster has different ranges, use its own ranges. `trustedProxyCIDRs`
must be within `podCIDRs`. The `uid` of the DNS and Gateway peers is a name
that you select. Their Pods carry it as a label, as the next section shows. The
`revision` fields are labels that you change each time that you change what
they describe.

The ingress entry selects the Pods of the AKS-managed ingress gateway by their
own label. As a result, you do not have to change this entry after a gateway
rollout. The ports are OpenClaw's UI (`18789`), the terminal (`7681`), and
Hermes' API and Dashboard (`8642`, `9119`). For its conditions, see
[rollout-stable managed ingress]({{< repo "examples/native-sandbox/README.md" >}}#rollout-stable-managed-ingress).
For every field, see the
[capability registration]({{< repo "app/cetirun-operator/README.md" >}}#retained-single-start-profile-and-capability-registration).

Create the ConfigMap. Protect it as cluster configuration. Anyone who can
write it decides what Agents can reach.

```bash
kubectl create configmap sandbox-platform -n cetirun-system --from-file=platform.json
```

## Peer labels

Cilium lets an Agent reach the Pods of the DNS and Gateway peers only when
these Pods carry the label `sandbox.cetirun.microsoft.com/peer-uid`. The value
of this label is the peer's `uid`. Their addresses alone are not sufficient.
Label CoreDNS and the AI Gateway:

```bash
kubectl label pods -n kube-system -l k8s-app=kube-dns \
  sandbox.cetirun.microsoft.com/peer-uid=coredns --overwrite
kubectl label pods -n cetirun-system \
  -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway \
  sandbox.cetirun.microsoft.com/peer-uid=ai-gateway --overwrite
```

The label belongs to the Pods, not to their Deployments. After an AKS update
replaces CoreDNS, label the new Pods again as soon as they start. Do this also
after every Gateway upgrade or restart. Until then, Agents' model requests and
name lookups fail. A label is not necessary on the Pods of the ingress gateway.

## Enforce admission and run Sandboxes

After admission answers, enforce it and enable Sandbox execution in one more
upgrade. Your values file then has:

```yaml
cetirun-operator:
  sandboxAdmission:
    enabled: true
    podsEnabled: true
    enforce: true
    tlsSecretName: cetirun-sandbox-admission-tls
    caBundle: <the contents of ca-bundle.txt>
    adminGroups:
      - "<admin-group-object-id>"
  sandboxSources:
    enabled: true
    nativeExecution: true
    bootstrapOwnedPrerequisites: true
    platformConfigMapName: sandbox-platform
cetirun-portal:
  # The Portal creates Sandbox Agents with the image and chart the operator
  # runs; without it, it needs sandboxRuntimeProfiles for each harness.
  sandboxRuntimeDefaultsFromController: true
```

Run the deployment helper again. A Sandbox Agent runs the operator's runtime
chart and the OpenClaw or Hermes image of the same release.

## Check the setup

In the Portal, under **Admin**:

1. **Sandboxes → New Sandbox**: select Kata, the `sandbox-home` StorageClass,
   and the default sizes. The form suggests a helper memory limit of `2Gi`.
   This limit leaves room for OpenClaw's initialization.
2. **Agent policies → New policy**: select the harnesses that Agents can run and
   the shared models that they can use. Also set their monthly Gateway budget.
   This budget is necessary if they can use a model.
3. **Teams → New Team**: select the Policy. Select the Sandbox as the allowed
   and default Sandbox.

Make sure that the Sandbox reports ready and that the Policy reports accepted:

```bash
kubectl get sandboxes.cetirun.microsoft.com,agentpolicies.cetirun.microsoft.com,teams.cetirun.microsoft.com
```

If a Sandbox is not ready, it names the cause in its conditions. An example is
a StorageClass that the registration does not pin (`UnapprovedStorage`). If an
Agent Policy is not accepted, it names the setting that the registration does
not support. Members can then create Agents for the Team, as in
[Your first instance](../../getting-started/first-instance/).

### Team operations rejected by admission

An HTTP 403 can come from the Sandbox admission webhook, even when the Portal
ServiceAccount has permission to create or update Teams. The Portal reports
these webhook refusals as `admission_rejected`, separately from
`rbac_forbidden`. Examine the selected Policy, Sandbox, and registered
topology. Then refer to the administrator-only Portal diagnostics. Do not
increase ClusterRole permissions only to solve a network conflict. The response
does not include raw webhook details, and admission and RBAC enforcement do not
change.

## What changes for Agents

- Tailscale is not available to Sandbox Agents.
- Custom harnesses, Entra projection, and Agent Identity/Connections must have
  the separate approved-image and identity setup in the
  [Custom contract](../../reference/agent-harness/#policy-governed-custom-execution).
  The basic configuration above does not enable them.
- A change to the model access, budget, network, or security settings of a
  Policy restarts its running Agents one time. A change to only its request
  limits does not.
- Concurrent updates to the shared native reservation use bounded retries for
  definite Kubernetes resource-version conflicts. Each retry reads the
  authority again. The retries do not bypass stop or source conflicts, or
  uncertain writes.
- An Agent moves to another Team only when an administrator reauthorizes it
  with `kubectl`.

Sources: [Sandbox capabilities]({{< repo "app/cetirun-operator/internal/sandbox/capabilities.go" >}}),
[network compiler]({{< repo "app/cetirun-operator/internal/sandboxpolicy/network.go" >}}),
[admission chart]({{< repo "app/cetirun-operator/chart/templates/sandbox-admission.yaml" >}}),
[native Sandbox example]({{< repo "examples/native-sandbox/README.md" >}}),
and the operator README's
[common Sandbox reconciliation]({{< repo "app/cetirun-operator/README.md" >}}#common-sandbox-reconciliation-explicit-opt-in).
