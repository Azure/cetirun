---
title: "Testing"
description: "Run component, chart, and documentation checks without contacting a cluster."
weight: 20
---

## Render the runtime chart

Run these commands from the repository root with Helm installed. They use the
local chart and synthetic values; they do not start an agent, pull runtime
images, verify credentials, or test AKS integrations.

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
helm template cetirun app/cetirun-chart \
  --set agentType=openclaw \
  --set-string secrets.model=example-model \
  --set-string secrets.openclawGatewayToken=dev-token
```

To inspect Hermes:

```bash
helm template cetirun app/cetirun-chart \
  --set agentType=hermes \
  --set-string secrets.model=example-model
```

`example-model` and `dev-token` are render-only placeholders. The chart requires
an explicit model. Do not apply this output to a cluster.

## Check the documentation

```bash
make -C site check
make -C site serve
```

The first run installs pinned site dependencies and may access package
registries. With those dependencies already installed, use `make docs-check`
for the repository's offline documentation gate or `make -C site test` for
the standard-library validator tests alone.

See the [site contributor guide](../../contributing/site/) for the pinned tool
versions, an existing-Hugo override, and the local preview address.

## Validate application and infrastructure changes

The root [Makefile]({{< repo "Makefile" >}}) and
[validation runner]({{< repo "scripts/ci/validate.py" >}}) provide:

| Command | Scope |
| --- | --- |
| `make test` | Go, frontend, auth/Teams, CI helper, and script tests |
| `make lint` | License headers, Python syntax, Go vet, frontend types, all five charts, Bicep, shell, and selected workflows |
| `make check` | `make lint` and `make test`; documentation is separate |
| `make chart-check` | Runtime chart for both agents, operator, Portal, Istio, and platform charts, plus runtime/platform/Istio render regressions |
| `make docs-check` | Site tests, build, links, and content checks |

Install the prerequisites separately: the Go version in
[`.go-version`]({{< repo ".go-version" >}}), Node.js 24 and the frontend's locked
npm dependencies, the Python packages in
[`scripts/ci/requirements.txt`]({{< repo "scripts/ci/requirements.txt" >}}),
and Helm, Bicep, ShellCheck, and actionlint. These gates fail on missing tools
or dependencies; they do not log in to Azure or run deployment scripts.
The [developer guide]({{< repo "DEVELOPMENT.md" >}}) describes component setup.

For example, these narrower checks need only Python's standard library:

```bash
make license-check ci-test python-check
python3 scripts/check-license-headers.py --root .
python3 scripts/ci/validate.py --help
```

Workflow lint covers an explicit validation-workflow list, not every workflow.
Image CVE scans and dependency review are separate CI jobs, not part of
`make check`. Go tests use a disposable embed fixture if the Portal frontend has
not been built. Run the frontend build separately to validate the production
bundle when changing the Portal.

## Integration testing

Use an authorized test environment to verify RuntimeClass availability,
storage provisioning, image access, Istio authentication, provider entitlement,
Teams permissions, and Tailscale connectivity. Local rendering does not
exercise these integrations.
