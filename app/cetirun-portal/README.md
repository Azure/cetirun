# CetiRun Portal

The Portal is the web application of CetiRun. Users create and manage their
Agents in it. Portal administrators manage Teams, Sandboxes, and
Agent Policies in it. The Portal has a Go backend and a React and TypeScript
frontend. The platform chart installs it with `make deploy`.

| Path | Contents |
| --- | --- |
| `main.go` and `internal/` | Go backend: API handlers, Kubernetes access, authentication, Chat proxy, and configuration |
| `web/` | React and TypeScript frontend. The Go binary embeds the built `web/dist`. |
| `web/licenses/` | [License supplements](web/licenses/README.md) for the third-party notice of the frontend |
| `chart/` | Helm chart of the Portal |
| `deploy/deploy.sh` | Helper that installs only the Portal chart. It needs an authorized cluster. |

## Develop

The backend reads its settings from environment variables. For the list and
the defaults, refer to [`internal/config/config.go`](internal/config/config.go).

For the checks, refer to [Portal](../../DEVELOPMENT.md#portal) in the developer
guide. To start the frontend or the backend on your workstation, refer to
[Run components locally](../../DEVELOPMENT.md#run-components-locally). The
backend connects to the cluster of your current kubeconfig context. Use only a
development cluster that you have authorization to change.

To lint, run `make portal-lint` and
`python3 -B scripts/ci/validate.py go-lint --component portal` at the
repository root. The `lint` target in this Makefile does not work. Refer to
[Root gates](../../DEVELOPMENT.md#root-gates).

## Use

For the user tasks, refer to the
[User Guide](../../site/content/en/docs/user-guide/_index.md). For the
installation, refer to
[Install CetiRun](../../site/content/en/docs/getting-started/platform.md).
