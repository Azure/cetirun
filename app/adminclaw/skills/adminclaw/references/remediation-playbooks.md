# Remediation Playbooks

Decision trees and playbooks for detecting and fixing common CetiRun platform failures.

---

## 1. Auto-Remediation Decision Tree

When a failure is detected (via health check or event watch), walk this tree to determine the correct action.

### Pod Failures

#### CrashLoopBackOff

```
Detected: status.containerStatuses[].state.waiting.reason == CrashLoopBackOff

1. Collect logs:
   kubectl logs -n <ns> <pod> --previous
   kubectl logs -n <ns> <pod> -c <container> --previous  (if multi-container)

2. Analyze exit reason:
   kubectl get pod -n <ns> <pod> -o jsonpath='{.status.containerStatuses[*].lastState.terminated}'

3. Decision:
   ├─ OOMKilled → See OOMKilled playbook below
   ├─ Config error (invalid env, missing secret) → Report to admin (requires confirmation)
   └─ Transient error (exit code 1, no clear config issue):
      a. Delete pod to let StatefulSet recreate: kubectl delete pod -n <ns> <pod>
      b. Wait for new pod to stabilize (Running + Ready for 60s)
      c. If crash recurs → escalate to admin
```

**Automation tier**: Autonomous for restart; escalate if crash persists after restart.

#### OOMKilled

```
Detected: status.containerStatuses[].lastState.terminated.reason == OOMKilled

1. Check current memory limits:
   kubectl get pod -n <ns> <pod> -o jsonpath='{.spec.containers[*].resources.limits.memory}'

2. Restart pod (autonomous):
   kubectl delete pod -n <ns> <pod>

3. Monitor for recurrence:
   - If OOMKilled >3 times in 1 hour → escalate to admin
   - Recommend resource limit increase (requires confirmation)
```

**Automation tier**: Restart is autonomous. Limit increase requires confirmation.

#### ImagePullBackOff

```
Detected: Pod event shows image pull failure

1. Check image reference:
   kubectl get pod -n <ns> <pod> -o jsonpath='{.spec.containers[*].image}'

2. Check events for details:
   kubectl describe pod -n <ns> <pod> | grep -A5 "Events:"

3. Report to admin — AdminClaw cannot fix registry access or image availability issues.
```

**Automation tier**: Always escalate.

---

### Helm Failures

#### Release Stuck in pending-upgrade / pending-install

```
Detected: helm status <release> -n <ns> shows pending-upgrade or pending-install

1. Identify stuck release:
   kubectl get secret -n <ns> -l owner=helm,status=pending-upgrade
   kubectl get secret -n <ns> -l owner=helm,status=pending-install

2. Check release history:
   helm history <release> -n <ns>

3. For pending-upgrade:
   a. Rollback to previous known-good revision:
      helm rollback <release> <previous-revision> -n <ns>
   b. Verify recovery:
      helm status <release> -n <ns>
      kubectl get pods -n <ns>

4. For pending-install:
   a. Uninstall the failed release:
      helm uninstall <release> -n <ns> --no-hooks
   b. Report to admin that reinstallation is needed
```

**Automation tier**: Rollback is autonomous. If rollback fails, escalate.

#### Release Failed

```
Detected: helm status shows failed

1. Inspect history:
   helm history <release> -n <ns>

2. Report failure details to admin with:
   - Failed revision number
   - Error message from helm
   - Pod status in the namespace
```

**Automation tier**: Always escalate.

---

### Network / Ingress Failures

#### VirtualService Misconfigured

```
Detected: Route check fails — expected 302 (OAuth redirect), got 404/503/timeout

1. Check current VirtualService:
   kubectl get virtualservice -n cetirun-<alias> -o yaml

2. Validate against expected pattern:
   - hosts: ["<alias>.<your-domain>"]
   - gateways: ["aks-istio-ingress/cetirun-gateway"]
   - destination host: openclaw-<alias>-cetirun.cetirun-<alias>.svc.cluster.local
   - destination port: 18789

3. If standard config and route is wrong:
   Re-apply correct VirtualService (autonomous)

4. If non-standard routing config:
   Report to admin (requires confirmation)
```

**Automation tier**: Autonomous for standard config; escalate for non-standard.

#### DNS Resolution Failure

```
Detected: DNS lookup for <alias>.<your-domain> fails

1. Check CoreDNS pods:
   kubectl get pods -n kube-system -l k8s-app=kube-dns

2. Report to admin — DNS infrastructure is outside AdminClaw's scope.
```

**Automation tier**: Always escalate.

---

### Storage Failures

#### PVC Nearly Full (>85%)

```
Detected: df -h via exec shows >85% usage on /home/vibe

1. Identify large files:
   kubectl exec -n <ns> <pod> -- du -sh /home/vibe/* | sort -rh | head -20

2. Autonomous cleanup:
   - Clean /tmp: kubectl exec -n <ns> <pod> -- rm -rf /tmp/*
   - Clean old logs: kubectl exec -n <ns> <pod> -- find /home/vibe -name "*.log" -mtime +7 -delete

3. Re-check usage after cleanup

4. If still >85% or >95%:
   Recommend PVC expansion (requires confirmation):
   kubectl patch pvc <pvc-name> -n <ns> --type=merge -p '{"spec":{"resources":{"requests":{"storage":"<new-size>"}}}}'
```

**Automation tier**: Cleanup is autonomous. PVC expansion requires confirmation.

#### PVC Stuck in Pending

```
Detected: PVC remains in Pending state

1. Check storage class:
   kubectl describe pvc <pvc> -n <ns>

2. Check events for provisioning errors

3. Report to admin — likely a storage class or capacity issue.
```

**Automation tier**: Always escalate.

---

### Auth / Token Failures

#### Gateway Token Expired or Missing

```
Detected: Auth failure on instance access, or Secret missing/empty

1. Generate new token:
   openssl rand -base64 32

2. Update (or create) Secret in namespace, preserving Helm ownership labels
   so that subsequent Helm upgrades do not delete it:
   kubectl create secret generic gateway-token -n cetirun-<alias> \
     --from-literal=OPENCLAW_GATEWAY_TOKEN=<new-token> \
     --dry-run=client -o yaml \
     | kubectl label -f - --local -o yaml \
         "app.kubernetes.io/managed-by=Helm" \
     | kubectl annotate -f - --local -o yaml \
         "meta.helm.sh/release-name=openclaw-<alias>" \
         "meta.helm.sh/release-namespace=cetirun-<alias>" \
     | kubectl apply -f -

3. Restart pod to pick up new token:
   kubectl delete pod -n cetirun-<alias> <pod>

4. Verify instance is accessible after restart
```

**Automation tier**: Autonomous.

### Namespace Stuck Terminating

```
Detected: kubectl get ns shows Terminating state >5 minutes

1. Identify blocking finalizers:
   kubectl get ns <ns> -o jsonpath='{.spec.finalizers}'

2. Check for resources with finalizers:
   kubectl api-resources --verbs=list --namespaced -o name \
     | xargs -I {} kubectl get {} -n <ns> --ignore-not-found

3. AdminClaw does NOT have patch/update permissions on Namespaces.
   Finalizer removal requires escalation:
   - Report blocking finalizers and stuck resources to admin
   - Provide the exact kubectl patch command the admin should run:
     kubectl patch ns <ns> --type=merge -p '{"spec":{"finalizers":[]}}'

4. Verify namespace deletion completes after admin intervention:
   kubectl get ns <ns>
```

**Automation tier**: Always escalate — AdminClaw cannot patch Namespaces.

---

## 2. Failure Modes Reference Table

Quick-reference table for all known failure modes.

| Failure Mode | Detection | Autonomous Fix | Escalation Trigger |
|--------------|-----------|---------------|-------------------|
| Pod OOMKilled | `lastState.terminated.reason == OOMKilled` | Restart pod | Recurring OOM (>3 in 1 hour) |
| CrashLoopBackOff | `state.waiting.reason == CrashLoopBackOff` | Collect logs, restart pod | Crash persists after restart |
| PVC >85% full | `df -h` via exec | Clean `/tmp`, old logs | Usage >95% or no cleanable files |
| Helm stuck pending-upgrade | `helm status` shows `pending-upgrade` | `helm rollback` to previous revision | Rollback fails |
| Helm stuck pending-install | `helm status` shows `pending-install` | `helm uninstall --no-hooks` + report | Uninstall fails |
| VirtualService misconfigured | Route check fails (no 302 redirect) | Re-apply standard VirtualService | Non-standard routing config |
| Namespace stuck Terminating | Namespace in Terminating >5min | Diagnose blocking finalizers, escalate to admin | AdminClaw cannot patch Namespaces |
| Gateway token expired | Auth failure on instance access | Generate new token, update Secret, restart | Token generation fails |
| Operator not running | Deployment shows 0 ready | Alert admin (do NOT modify operator) | — |
| Istio ingress unhealthy | Pods not Ready in `aks-istio-ingress` | Alert admin (do NOT modify Istio infra) | — |
| StatefulSet not progressing | Pod stuck in Pending >5min | Check node capacity, events | Scheduling failure |
| ImagePullBackOff | Pod event shows pull failure | Alert admin | — |
