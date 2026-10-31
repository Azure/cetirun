<p align="center">
  <img src="docs/logo.jpg" alt="CetiRun logo" width="280" />
</p>

# CetiRun

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

CetiRun hosts [OpenClaw](https://github.com/openclaw/openclaw)
and [Hermes](https://github.com/NousResearch/hermes-agent) on AKS. A Kubernetes
operator manages persistent agents. A self-service portal gives authenticated
access and lifecycle operations.

## Documentation

The reader documentation is in the [Hugo/Docsy site](site/README.md).
To preview the documentation locally, run this command:

```bash
make -C site serve
```

Open `http://localhost:1313/cetirun/`. To build and validate the site, run
`make -C site check`. Refer to the [site guide](site/README.md) for the tool
prerequisites and for an override that uses an installed Hugo. After you
prepare the dependencies, the root `make docs-check` command runs the offline
documentation gate. This does not deploy a public website.

| Start here | Documentation |
| --- | --- |
| Understand the runtime | [Overview](site/content/en/docs/overview/_index.md) |
| See planned improvements | [Roadmap](site/content/en/docs/overview/roadmap.md) |
| Create a workspace and use agents | [User Guide](site/content/en/docs/user-guide/_index.md) |
| Prepare a platform | [Install CetiRun](site/content/en/docs/getting-started/platform.md) |
| Configure and troubleshoot a deployment | [Operations](site/content/en/docs/operations/_index.md) |
| Find resource fields and Helm values | [Reference](site/content/en/docs/reference/_index.md) |
| Configure an Agent Harness | [Agent Harness](site/content/en/docs/reference/agent-harness.md) |
| Develop, test, or improve documentation | [Contributing](site/content/en/docs/contributing/_index.md) |
| Understand security and data flow | [Security](site/content/en/docs/security/_index.md) |

## Components

| Source | Purpose |
| --- | --- |
| [app/cetirun-platform](app/cetirun-platform/README.md) | Umbrella chart for the operator and portal |
| [app/cetirun-chart](app/cetirun-chart/README.md) | Agent StatefulSet, persistent storage, desktop, terminal, and logging |
| [app/cetirun-operator](app/cetirun-operator/README.md) | Agent and Team custom resources, and lifecycle reconciliation |
| [app/cetirun-portal](app/cetirun-portal) | Go backend and React self-service frontend |
| [app/adminclaw](app/adminclaw) | Administrative deployment material and operations skills |
| [infra](infra/README.md) | AKS, Blob NFS, and shared Istio authentication |
| [scripts](scripts/README.md) | Local validation, build, and connected helper entrypoints |

For a cloud deployment, configured identity, DNS, TLS, storage, capacity, and
accessible images and charts are necessary. If you render locally, that is not
a cloud installation. Refer to the
[platform checklist](site/content/en/docs/getting-started/platform.md).
The Kubernetes API exposes `Agent` and `Team`. Before you update an older
installation, examine the
[API contract](app/cetirun-operator/README.md#api-contract).

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
