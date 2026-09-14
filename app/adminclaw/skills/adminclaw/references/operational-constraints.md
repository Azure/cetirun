# Operational Constraints

Blast radius controls, rate limits, and audit logging format for AdminClaw operations.

---

## 1. Blast Radius Controls

These limits prevent cascading failures from a single bad operation.

| Control | Limit |
|---------|-------|
| Max concurrent mutating operations | 3 |
| Mandatory pause between batches | Wait for all in current batch to complete before starting next |
| Max instances in single delete batch | 5 |
| Upgrade canary count before full rollout | Minimum 2 |
| Rollout halt on failure | Immediate halt if any instance in batch fails |

### Rate Limits

| API | Limit | Scope |
|-----|-------|-------|
| Kubernetes API writes | 30/min | Across all operations |
| Pod exec operations | 10/min | Diagnostics |
| Helm operations | 5/min | Across all releases |

---

## 2. Audit Logging

Log every mutating operation as structured JSON with these fields:

| Field | Description |
|-------|-------------|
| `timestamp` | ISO 8601 |
| `actor` | Always `adminclaw` |
| `action` | `create` / `update` / `delete` / `restart` / `rollback` / `rotate` / `cleanup` |
| `resource_type` | Kubernetes resource kind or API endpoint |
| `resource_name` | Name of affected resource |
| `namespace` | Kubernetes namespace (empty for cluster-scoped) |
| `details` | Human-readable description |
| `result` | `success` / `failed` / `skipped` / `dry-run` |
| `automation_tier` | `autonomous` / `confirmed` |
| `request_id` | Correlation ID for grouping related operations |

Output the log entry after each mutating operation completes.
