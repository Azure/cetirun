---
title: "Testing"
description: "Run component, chart, and documentation checks that do not connect to a cluster."
weight: 20
---

## Render the runtime chart

With Helm installed, run these commands from the repository root. The commands
use the local chart and synthetic values. They do not start an agent, pull
runtime images, validate credentials, or test AKS integrations.

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
helm template cetirun app/cetirun-chart \
  --set agentType=openclaw \
  --set-string secrets.model=example-model \
  --set-string aiGateway.keySecretName=example-model-key \
  --set-string secrets.apiBase=http://gateway.example.com/v1 \
  --set-string secrets.openclawGatewayToken=dev-token
```

To examine Hermes:

```bash
helm template cetirun app/cetirun-chart \
  --set agentType=hermes \
  --set-string secrets.model=example-model \
  --set-string aiGateway.keySecretName=example-model-key \
  --set-string secrets.apiBase=http://gateway.example.com/v1 \
  --set-string hermes.env.HERMES_WEBAPP_PUBLIC_URL=https://example.com/ui/cetirun
```

All values are render-only placeholders. For the values that the chart must
have, refer to [Offline rendering](../../reference/helm/#offline-rendering). Do
not apply this output to a cluster.

## Check the documentation

```bash
make -C site check
make -C site serve
```

The first run installs pinned site dependencies and can access package
registries. If these dependencies are already installed, use `make docs-check`
for the repository's offline documentation gate. Or use `make -C site test` to
run only the standard-library validator tests.

Refer to the [site contributor guide](../../contributing/site/) for the pinned
tool versions, an override for an installed Hugo, and the local preview
address.

## Validate application and infrastructure changes

The root [Makefile]({{< repo "Makefile" >}}) and
[validation runner]({{< repo "scripts/ci/validate.py" >}}) give these commands:

| Command | Scope |
| --- | --- |
| `make test` | Go, frontend, auth/runtime, CI helper, and script tests |
| `make lint` | License headers, Python syntax, Go vet, frontend types, all five charts, Bicep, shell, and selected workflows |
| `make check` | `make lint` and `make test`. Documentation is separate. |
| `make chart-check` | Runtime chart for both agents, operator, Portal, Istio, and platform charts, plus runtime/platform/Istio render regressions |
| `make docs-check` | Site tests, build, links, and content checks |

Install these prerequisites separately:

- The Go version in [`.go-version`]({{< repo ".go-version" >}})
- Node.js 24 and the frontend's locked npm dependencies
- The Python packages in
  [`scripts/ci/requirements.txt`]({{< repo "scripts/ci/requirements.txt" >}})
- Helm, Bicep, ShellCheck, and actionlint

These gates fail if a tool or a dependency is not installed. They do not log in
to Azure or run deployment scripts. The
[developer guide]({{< repo "DEVELOPMENT.md" >}}) describes how to prepare the
components.

For example, only Python's standard library is necessary for these narrower
checks:

```bash
make license-check ci-test python-check
python3 scripts/check-license-headers.py --root .
python3 scripts/ci/validate.py --help
```

Workflow lint covers an explicit validation-workflow list, not every workflow.
Image CVE scans and dependency review are separate CI jobs, not part of
`make check`. If the Portal frontend is not built, Go tests use a disposable
embed fixture. When you change the Portal, run the frontend build separately to
validate the production bundle.

## Integration testing

Use an authorized test environment to examine RuntimeClass availability,
storage provisioning, image access, Istio authentication, provider entitlement,
and Tailscale connectivity. A local render does not test these integrations.
