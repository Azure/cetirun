# Instance Lifecycle Reference

Detailed workflows for creating, upgrading, decommissioning, and reconfiguring CetiRun agents. New agents default to Hermes; OpenClaw requires an explicit selection.

---

## 1. Batch Instance Creation

### Input

A list of user aliases with optional config overrides (displayName, team, email, helm values).

### Workflow

```
1. Validate each alias:
   - Format: ^[a-z][a-z0-9-]{0,62}$
   - Not already provisioned: kubectl get agent <alias>
   - No naming collisions with reserved names

2. Pre-flight checks:
   - Cluster resources: verify node capacity and storage availability
   - Operator is running: kubectl get deployment -n cetirun-system

3. Create Agent CRs (max 3 concurrent):
   apiVersion: cetirun.microsoft.com/v1alpha1
   kind: Agent
   metadata:
     name: <alias>
   spec:
     agent:
       type: hermes
     user:
       alias: <alias>
       displayName: <name>
       team: <team>
       email: <email>
     gatewayTokenSecretRef: "gateway-token"
     helm:
       values: <overrides>

4. Monitor CR status.phase transitions:
   Pending → Provisioning → Ready (or Failed)
   - Poll every 10s, timeout after 10min per instance
   - Use: kubectl get agent <alias> -o jsonpath='{.status.phase}'

5. Post-verify each Ready instance:
   a. Pod is Running and Ready:
      kubectl get pods -n cetirun-<alias>
   b. VirtualService resolves correctly:
      kubectl get virtualservice -n cetirun-<alias>
   c. HTTPS endpoint returns 302 (OAuth redirect):
      curl -s -o /dev/null -w "%{http_code}" https://<alias>.<your-domain>

6. Report summary:
   - Created: list of successful aliases
   - Failed: list with failure reasons
   - Already existed: list of skipped aliases
```

### Important Notes

- **Concurrency limit**: Create max 3 CRs at a time. Wait for all in the batch to reach a terminal state (Ready or Failed) before starting the next batch.
- **If >1 instance**: This requires admin confirmation before proceeding.
---

## 2. Health Check Procedure

### Platform Component Checks

```
1. Operator:
   kubectl get deployment -n cetirun-system
   → Verify desired == ready replicas

2. Portal (if deployed):
   kubectl get deployment -n cetirun-portal
   → Verify desired == ready replicas

3. Istio Ingress:
   kubectl get pods -n aks-istio-ingress
   → All pods should be Running and Ready
```

### Per-Instance Checks

```
1. List all instances:
   kubectl get agents.cetirun.microsoft.com -o wide

2. For each instance, evaluate:
   a. Phase: Pending / Provisioning / Ready / Failed / Deleting
   b. Conditions: Ready, HelmDeployed, IngressReady, PodReady, TailscaleReady
      kubectl get agent <alias> -o jsonpath='{.status.conditions}'
   c. StatefulSet readiness:
      kubectl get sts -n cetirun-<alias>
   d. Pod status (Running, restarts, OOMKills):
      kubectl get pods -n cetirun-<alias> -o wide

3. Storage check:
   kubectl exec -n cetirun-<alias> <pod> -- df -h /home/vibe

```

### Health Report Format

```
## Platform Health Report

### Platform Components
| Component | Status | Details |
|-----------|--------|---------|

### Instance Summary
- Total: N
- Healthy: N
- Degraded: N
- Failed: N

### Per-Instance Status
| Alias | Phase | Pod | Storage | Issues |
|-------|-------|-----|---------|--------|

### Recommendations
- ...
```

---

## 3. Chart Version Upgrade Rollout

### Pre-flight

```
1. Identify target instances:
   - All instances, OR
   - Filtered by: team, alias pattern, current chart version

2. Pre-flight checks:
   a. Verify new chart version exists in OCI registry
   b. Operator is running and healthy
   c. No instances currently in Provisioning or Failed state:
      kubectl get agents -o jsonpath='{range .items[?(@.status.phase!="Ready")]}{.metadata.name}{"\t"}{.status.phase}{"\n"}{end}'
```

### Canary Phase

```
3. Select 2 canary instances (prefer non-critical or admin-owned)
4. Update CR:
   kubectl patch agent <canary> --type=merge -p '{"spec":{"helm":{"version":"<new-version>"}}}'
5. Monitor reconciliation:
   - Watch phase transitions
   - Check all conditions reach True
   - Verify pod is running new image
6. Observe canary for stability (at least a few minutes)
```

### Batch Rollout

```
7. Update remaining instances in batches of 3:
   for each batch:
     a. Patch CRs in batch
     b. Wait for ALL in batch to reach Ready
     c. If any instance fails → HALT rollout immediately, report status
8. Post-rollout verification:
   a. All instances in Ready phase
   b. All pods running new image:
      kubectl get pods -n cetirun-<alias> -o jsonpath='{.items[0].spec.containers[0].image}'
   c. No increase in error rates
```

### Rollback

If upgrade fails:
```
1. Revert CR spec.helm.version to previous value
2. Monitor rollback reconciliation
3. If operator rollback fails, use Helm CLI as emergency fallback:
   helm rollback <release> <previous-revision> -n <ns>
```

---

## 4. Decommissioning

### Safe Teardown

Decommissioning removes an instance while preserving data by default.

```
1. Confirm with admin:
   - Which instances to decommission
   - Whether to preserve PVCs (default: yes)

2. Pre-decommission:
   a. Record current state for audit log

3. Delete the Agent CR:
   NOTE: AdminClaw cannot delete CRs (RBAC restriction).
   Instruct the admin to run:
     kubectl delete agent <alias>
   Or request another principal with delete permissions to do so.

4. Monitor teardown:
   - Operator will delete Helm release, VirtualService, RBAC resources
   - Namespace may take time to terminate (Istio sidecar cleanup)

5. Handle PVC:
   - If preserving: PVCs remain in the namespace
   - If not preserving: admin must explicitly delete the namespace
     (which deletes PVCs)

6. Verify cleanup:
   - Namespace is gone (or only PVCs remain)
   - VirtualService is removed
   - No stale DNS entries
```

---

## 5. Configuration Updates

### Helm Values Update

To change instance configuration (e.g., resource limits):

```
1. Identify the target field in spec.helm.values
2. Patch the CR:
   kubectl patch agent <alias> --type=merge \
     -p '{"spec":{"helm":{"values":{<key>:<value>}}}}'
3. Monitor operator reconciliation
4. Verify the change took effect in the running pod
```

### Common Config Changes

| Change | CR Path | Requires Confirmation |
|--------|---------|----------------------|
| Memory limit | `spec.helm.values.resources.limits.memory` | Yes |
| CPU limit | `spec.helm.values.resources.limits.cpu` | Yes |
| Tailscale auth | `spec.tailscale.authKeySecretRef` | Yes |
| Chart version | `spec.helm.version` | Yes |
