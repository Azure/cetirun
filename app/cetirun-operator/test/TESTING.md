# CetiRun Operator — Manual Testing Guide

This document describes how to manually deploy and test the `cetirun-operator` against a real Kubernetes cluster.

## Prerequisites

| Requirement | Notes |
|-------------|-------|
| `kubectl` configured to target cluster | `kubectl get nodes` should succeed |
| Operator image built and pushed |  |
| Istio installed (optional) | VirtualService tests require it; skip if absent |
| Azure Disk CSI driver and `default` StorageClass | Required for default HOME volumes — see Step 0 |
| Azure Blob CSI driver enabled (optional) | Required when enabling shared Blob skills storage |
| Dedicated workload nodepool | Node with label `workload=openclaw` and taint `workload=openclaw:NoSchedule` — see Step 0 |

---

## Step 0 — One-Time Cluster Setup

These steps are required once when setting up a new AKS cluster for cetirun-operator.

### 0a. Check HOME Storage and Optional Shared Skills

HOME defaults to `default`, `100Gi`, and `ReadWriteOnce`. On AKS, this is an
Azure Disk class with `WaitForFirstConsumer` binding:

```bash
kubectl get storageclass default
kubectl get csidriver disk.csi.azure.com
```

Shared skills still use `azureblob-nfs-premium`. If shared skills are enabled
and the Blob CSI driver is missing, enable the AKS blob storage addon:

```bash
az aks update \
  --resource-group <resource-group> \
  --name <aks-cluster-name> \
  --enable-blob-driver
```

Verify the driver pods are running before proceeding:

```bash
kubectl get pods -n kube-system | grep csi-azureblob
# Expected: csi-azureblob-controller-* and csi-azureblob-node-* pods Running
```

> **Note:** This operation takes a few minutes. The command updates the cluster in-place and
> does not restart existing workloads.

### 0b. Enable Istio (AKS Service Mesh Addon)

The operator creates Istio `VirtualService` resources to route external traffic to each instance.
Istio must be installed before the `cetirun-gateway` Gateway resource can be applied.

```bash
# Install Istio via AKS addon
az aks mesh enable \
  --name <aks-cluster-name> \
  --resource-group <resource-group> \
  --revision asm-1-27

# Enable the External Ingress Gateway
az aks mesh enable-ingress-gateway \
  --name <aks-cluster-name> \
  --resource-group <resource-group> \
  --ingress-gateway-type External
```

Verify:

```bash
kubectl get pods -n aks-istio-ingress
# Expected: aks-istio-ingressgateway-external-* pods Running

kubectl get svc -n aks-istio-ingress
# Expected: aks-istio-ingressgateway-external with an EXTERNAL-IP assigned
```

Then deploy the shared Istio infrastructure (Gateway, OAuth2 Proxy, EnvoyFilters) via `infra/istio/deploy.sh`.
The `cetirun-gateway` Gateway is created in `aks-istio-ingress` with HTTPS/443 for `*.example.com`.

```bash
cd infra/istio && ./deploy.sh
```

Verify:

```bash
kubectl get gateway -n aks-istio-ingress
# Expected: cetirun-gateway
```

### 0c. Add a Dedicated Workload Nodepool

OpenClaw instances are scheduled exclusively on nodes with `workload=openclaw`. Add a dedicated
nodepool and configure its labels and taints:

**Option A — Create nodepool with labels/taints in one command:**

```bash
az aks nodepool add \
  --resource-group <resource-group> \
  --cluster-name <aks-cluster-name> \
  --name workload \
  --node-count 1 \
  --node-vm-size Standard_D4s_v3 \
  --node-taints workload=openclaw:NoSchedule \
  --labels workload=openclaw
```

**Option B — Label and taint an existing node manually:**

```bash
# Replace <node-name> with the actual node name from `kubectl get nodes`
kubectl label node <node-name> workload=openclaw --overwrite
kubectl taint node <node-name> workload=openclaw:NoSchedule --overwrite
```

Verify:

```bash
kubectl get nodes -l workload=openclaw
kubectl describe node <node-name> | grep -A5 Taints
```

---

## Step 1 — Apply Cluster Prerequisites

Run once per cluster before installing the operator. The script checks the
StorageClass and Istio Gateway (if Istio is present), then installs the operator
via Helm. The chart creates the CetiRun CRDs on its first installation. Every
step is idempotent and safe to re-run.

```bash
cd app/cetirun-operator
./deploy/deploy.sh
```

---

## Step 2 — Deploy the Operator via Helm

```bash
# Create namespace (Helm won't create it automatically for the first install)
kubectl create namespace cetirun-system

# Install operator chart (from the repo root)
helm install cetirun-operator app/cetirun-operator/chart \
  --namespace cetirun-system \
  --set image.repository=... \
  --set image.tag=v... \
  --set leaderElection=true   # set true for production multi-replica

# The chart's crds/ directory creates CetiRun CRDs on first installation.
# Helm does not upgrade or delete CRDs; schema changes require a reviewed migration.

# Test cluster without access to the shared skills storage account (cetirunskills):
# Disable shared skills via spec.helm.values in the instance CR (see sample-alice.yaml)
```

> **Testing on another cluster?** If a StorageClass named `default` does not exist,
> override the StorageClass when creating instances via `spec.helm.values`:
> ```yaml
> helm:
>   values:
>     persistence:
>       storageClass: "approved-home"   # replace with an available StorageClass
> ```

### Verify deployment

```bash
kubectl get pods -n cetirun-system
# Expected: cetirun-operator-<hash>   1/1   Running

kubectl logs -n cetirun-system deployment/cetirun-operator
# Expected last line: "Starting workers"
```

---

## Step 3 — Create a Test Instance

```bash
kubectl apply -f app/cetirun-operator/test/config/sample-alice.yaml
```

### Watch reconciliation

```bash
# CR status (phase, conditions, accessUrl)
kubectl get agent alice -w

# Detailed view with events
kubectl describe agent alice

# Operator logs
kubectl logs -n cetirun-system deployment/cetirun-operator -f
```

### Expected progression

```
phase: Provisioning  →  namespace cetirun-alice created
                     →  secret gateway-token created
                     →  Helm release openclaw-alice installed
                     →  VirtualService cetirun-alice created (requires Istio)
phase: Ready         →  accessUrl: https://cetirun.example.com/ui/alice/
```

---

## Step 4 — Verify Created Resources

```bash
# Namespace
kubectl get namespace cetirun-alice

# Secrets
kubectl get secret -n cetirun-alice

# Gateway token content (base64-encoded random bytes)
kubectl get secret gateway-token -n cetirun-alice -o jsonpath='{.data.OPENCLAW_GATEWAY_TOKEN}' | base64 -d

# Helm release
helm list --namespace cetirun-alice
helm status openclaw-alice --namespace cetirun-alice

# OpenClaw pod
kubectl get pods -n cetirun-alice
kubectl logs -n cetirun-alice sts/openclaw-alice -c cetirun

# VirtualService (requires Istio) — lives in the per-engineer namespace
kubectl get virtualservice -n cetirun-alice cetirun-alice -o yaml
```

---

## Step 4b — Test Istio Ingress (End-to-End)

This step verifies the full traffic path: Internet → Istio Ingress Gateway → VirtualService → OpenClaw pod.

### Prerequisites

- `cetirun-gateway` Gateway applied (Step 0b)
- Instance is in `Ready` phase with `IngressReady=True`

### Get the Gateway IP and token

```bash
GATEWAY_IP=$(kubectl get svc aks-istio-ingressgateway-external \
  -n aks-istio-ingress \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

TOKEN=$(kubectl get secret gateway-token -n cetirun-alice \
  -o jsonpath='{.data.OPENCLAW_GATEWAY_TOKEN}' | base64 -d)

echo "Gateway IP: $GATEWAY_IP"
echo "Token: $TOKEN"
```

### Test with curl

```bash
# Health check — expect HTTP 200
curl -v "http://$GATEWAY_IP/?token=$TOKEN" \
  -H "Host: alice.example.com"
```

The `-H "Host: ..."` header is required because Istio VirtualService matches on the
`:authority` header (i.e. `Host`). Sending the request directly to the IP without this
header will result in a 404.

### Verify routing isolation

Each instance only accepts requests matching its own hostname. Confirm that bob's token
does not work on alice's hostname (expect 401 or 403):

```bash
BOB_TOKEN=$(kubectl get secret gateway-token -n cetirun-bob \
  -o jsonpath='{.data.OPENCLAW_GATEWAY_TOKEN}' | base64 -d)

curl -v "http://$GATEWAY_IP/?token=$BOB_TOKEN" \
  -H "Host: alice.example.com"
# Expected: 401 Unauthorized (wrong token for this instance)
```

### Verify VirtualService is in effect

```bash
kubectl get virtualservice -A
# Expected: cetirun-alice in cetirun-alice, cetirun-bob in cetirun-bob, etc.

kubectl get virtualservice cetirun-alice -n cetirun-alice -o yaml
# Check: hosts, gateways (istio-system/cetirun-gateway), route destination matches openclaw-alice-cetirun.cetirun-alice:18789
```

---

## Step 5 — Test Deletion

```bash
kubectl delete agent alice
```

Expected cleanup sequence (visible in operator logs):

1. Helm uninstall `openclaw-alice`
2. VirtualService `cetirun-alice` deleted from `cetirun-alice`
3. Namespace `cetirun-alice` deleted
4. Finalizer removed → CR deleted

```bash
# Confirm everything is gone
kubectl get namespace cetirun-alice        # NotFound
kubectl get agent alice         # NotFound
helm list --namespace cetirun-alice        # empty
```

---

## Step 6 — Cleanup Test Resources

```bash
# Delete all test instances (triggers finalizer cleanup for each)
kubectl delete agent --all

# Uninstall operator (removes Deployment, ServiceAccount, ClusterRole, ClusterRoleBinding)
helm uninstall cetirun-operator --namespace cetirun-system

# Remove namespace
kubectl delete namespace cetirun-system

# Remove cluster prerequisites (only if decommissioning the operator entirely)
kubectl delete crd agents.cetirun.microsoft.com
```

Do not delete the cluster-managed `default` StorageClass or shared skills
storage as part of operator cleanup. The default Azure Disk reclaim policy
can delete disks when their PVCs are deleted; preserve any required test data.

---

## Troubleshooting

| Symptom | Check |
|---------|-------|
| Pod stuck in `Provisioning` | Check operator logs for Helm install errors |
| `HelmInstallFailed` condition | `helm status openclaw-<alias> --namespace cetirun-<alias>`; check the configured OCI chart reference and pull access |
| `VirtualServiceFailed` condition | Expected if Istio is not installed; safe to ignore for non-ingress testing |
| PVC unbound / pod Pending | `kubectl describe pvc -n cetirun-<alias>`; check the StorageClass and Pod scheduling events. `WaitForFirstConsumer` needs a schedulable Pod; review storage overrides if the class is absent. |
| Pod crashes on startup | Check `kubectl describe pod -n cetirun-system`; likely image pull or secret mount issue |
| `couldn't find key OPENCLAW_GATEWAY_TOKEN in Secret` | The `gateway-token` secret must use `OPENCLAW_GATEWAY_TOKEN` as the key name. If you pre-created the secret manually, recreate it: `kubectl create secret generic gateway-token -n cetirun-<alias> --from-literal=OPENCLAW_GATEWAY_TOKEN=<token>` |
| CR stuck with finalizer after `kubectl delete` | Check operator logs; if operator is down, manually remove finalizer: `kubectl patch agent <name> -p '{"metadata":{"finalizers":[]}}' --type=merge` |
| GitHub Copilot auth not working | Each engineer must run `copilot-auth` (or `openclaw models auth login-github-copilot`) inside their OpenClaw pod to authenticate with GitHub Copilot. |
