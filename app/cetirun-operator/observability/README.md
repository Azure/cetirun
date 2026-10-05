# Opt-in Sandbox reported-status alerts

These standalone Prometheus rule examples use only the public
[reported-status metrics contract](../README.md#sandbox-reported-status-metrics)
and [collector](../internal/controller/sandbox_metrics.go), plus Prometheus's
scrape-generated `up` metric. Nothing loads or deploys them automatically.
They add no scraper, listener, Service, CRD, `PrometheusRule`, chart integration,
or default CI check. Configure authorized scraping and rule loading separately;
the operator's metrics endpoint is plaintext and unauthenticated.

**Cached reports are not live enforcement signals.** Each operator replica
reports an independently cached, non-atomic view of the same population.
Successful collection, `up=1`, a `True` condition, or an alert resolving does not
prove fresh observations, runtime qualification, execution safety or containment.
There is no observation-age metric in this contract.

## Adapt before loading

Review [the rules](sandbox-alerts.rules.yml) before explicitly loading them into
an existing, authorized Prometheus installation:

- Replace the synthetic `job="example-cetirun-operator"` selector everywhere.
  Select the **operator**, not the separate admission process.
- Supply nonempty `cluster`, `fleet`, `job` and `instance` labels on `up` and
  both Sandbox metrics at the rule evaluator. The full tuple must identify one
  scrape target in one population. `cluster` and `fleet` are example
  scraper-supplied identity labels, not labels exported by the operator.
  Prometheus `external_labels` alone do not add labels to local query inputs.
- Keep those identity labels identical across all three metrics. Other labels
  may differ; joins explicitly use the identity tuple, while alerts retain
  their source series' other labels, including `runtime`, `class`, `status`
  and `reason` for reported loss. Do not add per-Agent/object labels.
- If additional labels distinguish independent populations or monitoring
  sources, extend the required-label checks, **every join and expected-target
  selector** accordingly. Require at most one `up` and one collection gauge
  per identity tuple. Multiple population groups per tuple are expected; their
  metric dimensions remain separate. Resolve duplicate monitoring sources in
  the monitoring configuration; these examples do not infer topology or
  deduplicate remote-write/HA monitoring replicas.
- Replace the explicit inventory in `CetiRunSandboxTargetMissing`. It expects
  two synthetic replicas in each of two populations:

  | `cluster` | `fleet` | Expected `instance` values |
  | --- | --- | --- |
  | `example-a` | `example-blue` | `operator-0.example.com:8080`, `operator-1.example.com:8080` |
  | `example-b` | `example-green` | `operator-0.example.com:8080`, `operator-1.example.com:8080` |

  All use the example job above. Overlapping job/instance labels across
  populations are intentional. The inventory is illustrative, not a deployment
  assumption. Extra targets in the selected job are checked while present, but
  their complete disappearance cannot be detected without adding expectations.
  Update expectations for deliberate target removal.
- Preserve both metric families when scraping/relabeling. A successful
  collection with no population series means empty/legacy-only **only under
  that contract**. Filtering out just `cetirun_sandbox_agents` is
  indistinguishable from an empty population with these signals.
- Review routing, severity and timing with the deployment's owners. Every
  example uses `severity: warning`, a one-minute evaluation interval and a
  continuous `for: 5m` debounce. Five minutes is illustrative, not an operational
  SLO, observation-freshness limit or authorized response deadline.

There is no summation, averaging or best-replica selection of population
snapshots. A healthy replica/fleet must not mask a failed one. Route or group
notifications as appropriate without dropping source identity from the alert
data. The identity-label rule uses `group without (__name__)` only to flag the
presence of malformed label sets; it never combines population counts.

## Meaning and response guidance

Inspect only the selected target/population through authorized monitoring,
current status and trusted operator logs. Do not publish raw logs or object
identifiers.

| Alert or observation | Meaning and conservative response |
| --- | --- |
| `CetiRunSandboxTargetIdentityMissing` | A selected `up` or Sandbox series lacks a required identity label. Fix labeling before interpreting joins; unidentifiable status series are excluded from the joined alerts, not treated as healthy. |
| `CetiRunSandboxTargetMissing` | An explicitly expected target has no `up` series. Check target discovery, relabeling, ingestion and intended removal. This does not identify whether the operator is running. |
| `CetiRunSandboxScrapeFailed` | `up=0`: the scrape failed. Inspect the target's scrape error and approved connectivity/access path. Do not interpret retained status samples as current. |
| `CetiRunSandboxCollectionMetricMissing` | `up=1` but the collection gauge is absent for that identity. Check target selection, endpoint/version, disabled metrics and relabeling/filtering. Population counts are unavailable, not zero. |
| `CetiRunSandboxCollectionUnavailable` | `up=1` and collection success is `0`. Startup/cache initialization, synchronization or cached reads are unavailable/failed; counts are omitted. Inspect operator health, trusted logs and authorized API connectivity/RBAC. |
| `up=1`, collection success `1`, no population series | Empty/legacy-only reported population, assuming no metric filtering. No failure alert. This is not evidence that every intended Agent was discovered or that enforcement is current. |
| Held, suspended or deleting reports | `SandboxPrepared`, `SandboxPreparationPending` and `EnforcementPending` are not automatically failures; prepared still means zero-replica held preparation. `Suspended`, `Deleting` and `InstanceDeleting` are intentional lifecycle categories. These examples do not alert on them. |
| Other non-ready/unknown reports | Inspect current status and the public contract's response guidance. Missing/stale/malformed conditions and other reasons are not automatically enforcement loss. No generic non-ready ratio or failure alert is supplied. |
| `CetiRunSandboxReportedEnforcementLost` | A populated group has `status=False` or `Unknown`, `reason=EnforcementLost` and count **greater than zero**, with `up=1` and collection success `1` for that same identity. Investigate the affected control via trusted status/logs and the qualified response procedure. A `True` report with that reason does not meet this rule. |

The loss alert remains per target and per bounded status group, not per object.
Its threshold is exactly `> 0` (one reported Agent is enough), not a fleet ratio.
Collection problems suppress that interpretation and produce the corresponding
availability alert instead. A false or absent expression resets the five-minute
pending timer and clears a firing alert on the next evaluation; there is no
`keep_firing_for`. Resolution may mean missing evidence or changed lifecycle,
not restored enforcement. Availability alerts also have the five-minute debounce,
so a newly unavailable target can temporarily have no firing alert.

Missing series are subject to Prometheus staleness/lookback handling before
`absent` or `unless` can detect them; this is not a guarantee of detection within
five minutes of a real-world failure. If the rule evaluator/monitoring system
itself is unavailable, these rules cannot notify. Monitoring that system and
maintaining an expected-target inventory are separate responsibilities.

Never remove holds/finalizers, edit binding identities, bypass admission, revoke
grants or restart workloads solely because of these alerts. They neither stop
execution nor establish containment. Follow the qualified recovery procedure
and preserve access controls. For suspected vulnerabilities, use
[the security reporting instructions](../../../SECURITY.md), not public issues.

## Offline validation

Use **Prometheus 3.5.0 `promtool`**; prepare the public, checksum-verified release
tool separately if missing. No installation or download is part of these checks.
From the repository root, with that binary on `PATH`:

```bash
cd app/cetirun-operator/observability
promtool --version
promtool check rules sandbox-alerts.rules.yml
promtool test rules sandbox-alerts.test.yml
```

The [real rule tests](sandbox-alerts.test.yml) exercise PromQL and alert state:
zero/one thresholds, the instant before and at the five-minute firing boundary,
pending reset, recovery, duplicate snapshots from two operator replicas, two
populations with overlapping job/instance labels, unavailable/missing collectors,
absent targets, empty and intentional lifecycle populations, and heterogeneous
labels. Synthetic lingering samples verify that successful scrape and collection
signals are both required before interpreting reported loss.

These offline tests do not validate scraping, authentication, deployment,
notification delivery, running workloads or enforcement. They do not qualify
these illustrative thresholds for production.
