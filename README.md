<p align="center">
  <img src="docs/logo.jpg" alt="CetiRun logo" width="280" />
</p>

# CetiRun

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

CetiRun runs [OpenClaw](https://github.com/openclaw/openclaw),
[Hermes](https://github.com/NousResearch/hermes-agent), and your own agent
images as long-lived Agents for the people in your organization. It runs on
Azure Kubernetes Service (AKS). Your platform team installs and operates it in
your own Azure subscription.

## Why CetiRun

When each person runs an agent on their own computer, each person must manage
the agent, its model keys, and its data. CetiRun runs these agents for many
users on one shared AKS cluster:

- **Access:** Users sign in with Microsoft Entra ID. The ingress gateway lets
  only the owner of an Agent open it. This rule needs a unique alias for each
  user. The alias is the part of the sign-in name before `@`.
- **Isolation:** Each Agent runs in its own namespace, in a Kata VM by default,
  and keeps a private, persistent HOME volume.
- **Models:** The operator configures each Agent to call models through the AI
  Gateway. Each Agent has its own key and a monthly budget. The Agent Policy of
  a Sandbox Agent also sets rate limits.
- **Governance:** For the Agents of policy-bound Teams, Agent Policies and
  Sandboxes control agent types, models, skills, identity, and network access.
  These Agents need Sandbox execution, which is off by default.
- **Connections:** Owners give an Agent access to GitHub, Google, or Azure. The
  Agent Identity Broker keeps the credentials outside the Agent.

The Kubernetes API has the cluster-scoped resources `Agent`, `Team`,
`Sandbox`, and `AgentPolicy`. For the features and the deployment requirements,
read the [Overview](site/content/en/docs/overview/_index.md).

## Documentation

The documentation is a Hugo site, and the site is not published. The links
below open its Markdown sources. On GitHub, some links between pages and some
links to source files do not work in these files. To read the site with
working links, run this command and open `http://localhost:1313/cetirun/`:

```bash
make -C site serve
```

| Start here | Documentation |
| --- | --- |
| Learn what CetiRun does | [Overview](site/content/en/docs/overview/_index.md) |
| Use an Agent on a deployment that exists | [User Guide](site/content/en/docs/user-guide/_index.md) |
| Install CetiRun | [Install CetiRun](site/content/en/docs/getting-started/platform.md) |
| Configure and troubleshoot a deployment | [Operations](site/content/en/docs/operations/_index.md) |
| Review access, credentials, and data handling | [Security](site/content/en/docs/security/_index.md) and [PRIVACY.md](PRIVACY.md) |
| Run your own agent image | [Agent Harness](site/content/en/docs/reference/agent-harness.md) and [Examples](site/content/en/docs/reference/examples.md) |
| Find resource fields and Helm values | [Reference](site/content/en/docs/reference/_index.md) |
| Find the meaning of a term | [Glossary](site/content/en/docs/concepts/glossary.md) |
| See planned improvements | [Roadmap](site/content/en/docs/overview/roadmap.md) |
| Develop, test, or improve documentation | [Contributing](site/content/en/docs/contributing/_index.md) |

To build and validate the site, run `make -C site check`. The
[site guide](site/README.md) lists the tool prerequisites and an override that
uses an installed Hugo. After you prepare the dependencies, the root
`make docs-check` command runs the offline documentation checks.

## Components

| Source | Purpose |
| --- | --- |
| [app](app/README.md) | The deployable components: the operator, the Portal, the AI Gateway, the agent chart, the Agent Identity Broker, Connections MCP, Memory, and the platform and observability charts. [Architecture](site/content/en/docs/concepts/architecture.md) describes each one |
| [CetiAdmin](site/content/en/docs/operations/cetiadmin.md) | Hermes administration Agent, operations skills, and helpers deployed by the operator. The source is in `app/cetirun-operator/internal/cetiadmin` |
| [infra](infra/README.md) | Bicep for AKS and Blob NFS, the shared Istio authentication chart, and cert-manager configuration |
| [scripts](scripts/README.md) | Local validation, build, and connected helper entrypoints |
| [examples](examples) | Custom Agent examples and Sandbox platform checks |

A local render checks only the chart templates. A cloud installation also needs
identity, DNS, TLS, storage, capacity, and a registry with the CetiRun images
and charts. Refer to the
[platform checklist](site/content/en/docs/getting-started/platform.md). Before
you update an older installation, read
[Existing installations](site/content/en/docs/reference/crds.md#existing-installations).

## Contributing and security

Start with the [contributor guide](site/content/en/docs/contributing/_index.md).
To report vulnerabilities, use the process in [SECURITY.md](SECURITY.md). Do
not share credentials or private deployment data in a public issue. For
information about how the runtime handles data, read [PRIVACY.md](PRIVACY.md).
For third-party attributions and unresolved distribution work, read
[NOTICE](NOTICE).

## License

[MIT](LICENSE)

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of Microsoft trademarks or logos is subject to and must follow [Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general). Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or imply Microsoft sponsorship. Any use of third-party trademarks or logos are subject to those third-party's policies.
