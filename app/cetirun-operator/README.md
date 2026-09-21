# CetiRun operator

The operator reconciles `Agent` and `Team` resources in
`cetirun.microsoft.com/v1alpha1`.

- [Custom-resource reference](../../site/content/en/docs/reference/crds.md)
- [Agent Harness contract](../../site/content/en/docs/reference/agent-harness.md)
- [Operator chart configuration](../../site/content/en/docs/reference/helm.md#operator-chart)
- [Architecture and resource names](../../site/content/en/docs/concepts/architecture.md)
- [Platform installation prerequisites](../../site/content/en/docs/getting-started/platform.md)
- [Lifecycle and deletion boundaries](../../site/content/en/docs/guides/lifecycle.md)

## API contract

`Agent` (`agents`, short name `cra`) and `Team` (`teams`, short name `crt`) are
cluster-scoped. An Agent is a long-lived user-owned environment; a Team
supplies shared configuration, not access to private agent data.

The API replaces `OpenClawInstance` and `OpenClawTeam` without changing the
group or version. Installing these CRDs does not convert, adopt, or delete
legacy objects. Do not delete legacy CRs or CRDs as an upgrade shortcut:
finalizers may remove workloads and namespaces, and deleting a CRD removes
its stored objects. Existing installations need a reviewed data-retention and
ownership-handover plan. See the
[compatibility guidance](../../site/content/en/docs/reference/crds.md#existing-installations).

Source layout: `api/` defines the types, `config/crd/bases/` contains schemas,
`internal/` contains reconciliation and Helm/Azure integration, and `chart/`
deploys the operator. The chart packages CRDs under `chart/crds/`; Helm installs
them initially but does not upgrade or delete them. Follow
[DEVELOPMENT.md](../../DEVELOPMENT.md#generated-files-and-dependencies) for
pinned generation commands.

`deploy/deploy.sh` is a connected, mutating helper, not a local smoke test.
For a Gateway in a separate namespace, configure the
[Gateway reference and workload namespace](../../site/content/en/docs/reference/helm.md#operator-chart)
independently.
