---
title: "Run local checks"
linkTitle: "Testing"
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

After you change documentation, run the offline documentation gate:

```bash
make docs-check
```

It runs the validator tests, builds the site, and validates links, navigation,
and content. Before the first run, install the site dependencies with
`make -C site build`. That step needs network access. For the setup, the
overrides, and the preview, refer to
[Contribute to documentation](../../contributing/site/).

## Validate application and infrastructure changes

The root [Makefile]({{< repo "Makefile" >}}) gives these commands. Most of them
are gates of the [validation runner]({{< repo "scripts/ci/validate.py" >}}):

| Command | Scope |
| --- | --- |
| `make test` | Python CI helper and script tests, auth and runtime tests, Go tests of every module, Portal frontend tests, and the Memory Context native test |
| `make lint` | Release version and inventory checks, license headers, Python syntax, Go vet, the Portal TypeScript check, `make chart-check`, Bicep, shell scripts, and the validation workflows |
| `make check` | `make lint` and `make test`. Documentation is separate. |
| `make chart-check` | Lint and render of every first-party chart, both runtime chart variants, and the render regression scripts |
| `make docs-check` | Site tests, build, links, and content checks |

For the full list of what each gate runs, refer to
[Root gates]({{< repo "DEVELOPMENT.md" >}}#root-gates) in the developer guide.

Install the prerequisites first: the Go version in
[`.go-version`]({{< repo ".go-version" >}}), Node.js 24, Python 3.11 or later
with the packages in
[`scripts/ci/requirements.txt`]({{< repo "scripts/ci/requirements.txt" >}}),
and the tools in [`scripts/ci/tools.json`]({{< repo "scripts/ci/tools.json" >}}).
For the commands, refer to
[Tools and dependencies]({{< repo "DEVELOPMENT.md" >}}#tools-and-dependencies).
`make license-check` and `make python-check` need only Python. The other
Python gates also need the packages in `scripts/ci/requirements.txt`.

The gates run offline on your workstation, without an Azure login or
deployment scripts. A missing tool or dependency fails the gate. Image CVE
scans, `govulncheck`, CodeQL, and dependency review run only in CI.

To find the checks for the paths that you changed, including a check of one Go
module, refer to
[Checks for each change]({{< repo "DEVELOPMENT.md" >}}#checks-for-each-change).
To find the local command for a CI job, refer to
[CI jobs and local commands]({{< repo "DEVELOPMENT.md" >}}#ci-jobs-and-local-commands).

## Integration testing

A local render does not test RuntimeClass availability, storage provisioning,
image access, Istio authentication, provider entitlement, or Tailscale
connectivity. These tests need a live environment.

Maintainers run integration and E2E tests in an authorized environment. Run
them only with explicit authorization for the target environment. For the
tests and their inputs, refer to
[Integration and E2E tests]({{< repo "DEVELOPMENT.md" >}}#integration-and-e2e-tests).
