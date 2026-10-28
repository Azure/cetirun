---
title: "Policy-bound Teams"
description: "Prepare a cluster so that the Agents of a Team with an Agent Policy start in their Sandbox."
weight: 15
---

A Team created in the Portal has an Agent Policy and one or more Sandboxes.
Its Agents run in a Sandbox: each in a Kata virtual machine, with the Policy's
network rules compiled into Cilium network policies and every write checked by
the Sandbox admission webhook. The platform chart leaves this off. Until it is
on, the Portal refuses to create an Agent for such a Team, and an Agent created
for one with `kubectl` is held and never starts.

This page turns it on for an installation made with
[Install CetiRun](../../getting-started/platform/), in the release namespace
`cetirun-system`:

1. [Check the cluster](#the-cluster).
2. [Create a HOME StorageClass](#home-storage).
3. [Issue the admission certificate](#the-admission-certificate).
4. [Turn on admission without enforcement](#admission-without-enforcement).
5. [Register the platform](#the-platform-registration).
6. [Label the peers](#peer-labels).
7. [Enforce admission and run Sandboxes](#enforce-admission-and-run-sandboxes).
8. [Check with a Sandbox, a Policy and a Team](#check-the-setup).

Then follow [Smart routing](../smart-routing/) for a Team whose Agents use a
smart-routing model, or [Configure a team](../../guides/teams/) for the other
Team settings.

## The cluster

- **Kubernetes 1.37 or later.** The operator chart refuses Sandbox admission on
  an older cluster. The [cluster template](../infrastructure/) defaults to
  `1.34`: set `kubernetesVersion` in your parameter file to a 1.37 or later
  version your region offers.
- **Kata.** Agent node pools with `workloadRuntime: KataVmIsolation`, as the
  template's two agent pools have, provide the `kata-vm-isolation`
  RuntimeClass.
- **Cilium.** Azure CNI powered by Cilium, the template's networking. The
  operator writes a CiliumNetworkPolicy for each Agent.
- **Two nodes for the admission server.** Its two replicas run on different
  nodes, and every write to Agents, Teams, Sandboxes and Agent Policies waits
  for one of them.
- **ReadWriteOncePod storage.** Each Agent's HOME is a volume only its Pod may
  mount. Azure Disk supports it.

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

A Sandbox names this class, and the platform registration below pins it.

## The admission certificate

The admission server serves TLS under its Service name,
`cetirun-sandbox-admission.cetirun-system.svc`. The chart does not issue a
certificate: create a CA, a serving certificate for that name, and a TLS
Secret, for example with OpenSSL:

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
outside the cluster, and renew the certificate before it expires: an expired
certificate makes the admission server unready and refuses every write it
checks. The operator README describes
[rotating the CA]({{< repo "app/cetirun-operator/README.md" >}}#availability-and-bootstrap).

## Admission without enforcement

Admission has to run and answer before it may refuse anything: it cannot be
installed enforced. Add to your platform values file, and run the deployment
helper again:

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

List at least one administrator in `adminUsers` or `adminGroups`: only they
can create policy-bound configuration with `kubectl`, and only they can
reauthorize an Agent, for which the Portal has no page. Then check that both
replicas are ready, on different nodes:

```bash
kubectl rollout status deployment/cetirun-sandbox-admission -n cetirun-system
kubectl get pods -n cetirun-system -l app.kubernetes.io/name=cetirun-sandbox-admission -o wide
```

## The platform registration

The operator and the admission webhook read what the cluster provides from a
ConfigMap you write, `platform.json` in `sandbox-platform` in the operator's
namespace. They accept a Sandbox only with the RuntimeClass and StorageClass
pinned here, and build each Agent's network policy from the peers here: DNS,
the AI Gateway and the ingress gateway. Unknown fields, and fields left out
that a Sandbox needs, are refused.

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

Fill in the placeholders from the cluster:

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
| `<STORAGECLASS_UID>` | `kubectl get storageclass sandbox-home -o jsonpath='{.metadata.uid}'`; `Provisioner` is its `provisioner` |

The CIDRs are the cluster template's defaults: Pods `10.244.0.0/16`, Services
`172.16.0.0/16`, and the nodes' AKS-managed virtual network `10.224.0.0/12`.
Use your cluster's own ranges if they differ; `trustedProxyCIDRs` has to lie
within `podCIDRs`. The `uid` of the DNS and Gateway peers is a name you choose:
their Pods carry it as a label, below. The `revision` fields are labels you
change whenever you change what they describe.

The ingress entry selects the AKS-managed ingress gateway's Pods by their own
label, so a gateway rollout needs no change here. The ports are OpenClaw's UI
(`18789`), the terminal (`7681`), and Hermes' API and Dashboard (`8642`,
`9119`). See [rollout-stable managed ingress]({{< repo "examples/native-sandbox/README.md" >}}#rollout-stable-managed-ingress)
for its conditions, and the
[capability registration]({{< repo "app/cetirun-operator/README.md" >}}#retained-single-start-profile-and-capability-registration)
for every field.

Create the ConfigMap, and protect it as cluster configuration: whoever can
write it decides what Agents may reach.

```bash
kubectl create configmap sandbox-platform -n cetirun-system --from-file=platform.json
```

## Peer labels

Cilium selects the Pods of the DNS and Gateway peers by the label
`sandbox.cetirun.microsoft.com/peer-uid`, whose value is the peer's `uid`; an
Agent can reach no Pod without it. Label CoreDNS and the AI Gateway:

```bash
kubectl label pods -n kube-system -l k8s-app=kube-dns \
  sandbox.cetirun.microsoft.com/peer-uid=coredns --overwrite
kubectl label pods -n cetirun-system \
  -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway \
  sandbox.cetirun.microsoft.com/peer-uid=ai-gateway --overwrite
```

The label belongs to the Pods, not to their Deployments: label the new Pods
again after an AKS update replaces CoreDNS, and after every Gateway upgrade or
restart, as soon as the new Pods start. Until then, Agents' model requests and
name lookups fail. The ingress gateway's Pods need no label.

## Enforce admission and run Sandboxes

Once admission answers, enforce it and turn on Sandbox execution in one more
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

1. **Sandboxes → New Sandbox**: Kata, the `sandbox-home` StorageClass, the
   default sizes. The helper memory limit of `2Gi` the form suggests leaves
   room for OpenClaw's initialization.
2. **Agent policies → New policy**: the harnesses Agents may run, the shared
   models they may use, and their monthly Gateway budget, which is required
   once they may use a model.
3. **Teams → New Team**: the Policy, and the Sandbox as the allowed and
   default Sandbox.

The Sandbox has to report ready and the Policy accepted:

```bash
kubectl get sandboxes.cetirun.microsoft.com,agentpolicies.cetirun.microsoft.com,teams.cetirun.microsoft.com
```

A Sandbox that is not ready names the cause in its conditions, such as a
StorageClass the registration does not pin (`UnapprovedStorage`); an Agent
Policy that is not accepted names the setting the registration does not
support. Members can then create Agents for the Team, as in
[Your first instance](../../getting-started/first-instance/).

## What changes for Agents

- Tailscale, Microsoft Teams, Agent Identity, the desktop and custom harnesses
  are not available to Sandbox Agents.
- Changing a Policy's model access, budget, network or security settings
  restarts its running Agents once. Changing only its request limits does not.
- An Agent cannot move to another Team; an administrator reauthorizes it with
  `kubectl`.

Sources: [Sandbox capabilities]({{< repo "app/cetirun-operator/internal/sandbox/capabilities.go" >}}),
[network compiler]({{< repo "app/cetirun-operator/internal/sandboxpolicy/network.go" >}}),
[admission chart]({{< repo "app/cetirun-operator/chart/templates/sandbox-admission.yaml" >}}),
[native Sandbox example]({{< repo "examples/native-sandbox/README.md" >}}),
and the operator README's
[common Sandbox reconciliation]({{< repo "app/cetirun-operator/README.md" >}}#common-sandbox-reconciliation-explicit-opt-in).
