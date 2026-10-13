# AgentPolicy administration boundary

CetiRun already defines the cluster-scoped `AgentPolicy` custom resource. The
admin Portal provides lifecycle management for a deliberately small subset of
that resource: allowed harnesses, allowed skills, Copilot access, shared model
names, and the LiteLLM monthly budget expressed in US dollars.

The Portal does not bind a Policy to a Team and does not interpret or enforce a
Policy. Sandbox and AI Gateway components remain responsible for the fields
they execute. Editing a Policy that was bound outside the Portal can still
affect those consumers.

## HTTP API

The following admin-only endpoints manage `AgentPolicy` resources:

- `GET` and `POST /api/v1/admin/policies`
- `GET`, `PUT`, and `DELETE /api/v1/admin/policies/{name}`

Update and delete requests include the UID and resource version returned by a
previous read. Stale requests fail with `409 Conflict`. Updates patch only the
Portal-managed fields and preserve security, network, identity, Kubernetes
access, and other component-owned fields.

The API validates model access and budgets as one unit: Copilot or shared model
access requires a positive default and maximum monthly USD budget, and the
default cannot exceed the maximum.

## Activation boundary

The Portal intentionally provides no control for `Team.spec.agentPolicyRef`,
`sandboxRefs`, or `defaultSandboxRef`. Team binding should be exposed only
after the Sandbox and AI Gateway owners have completed and validated their
respective execution paths.
