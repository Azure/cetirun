<!-- Copyright (c) Microsoft Corporation. Licensed under the MIT License. -->

# CetiRun documentation site

Build and preview the CetiRun documentation with Hugo and Docsy. Component
READMEs link to the applicable pages in this site. For the setup, the checks,
the overrides, and the authoring rules, refer to the
[contributor page](content/en/docs/contributing/site.md).

Run these commands from the repository root. The first command needs network
access. It installs Hugo, the Hugo modules, and the npm packages:

```bash
make -C site build
make docs-check
make -C site serve
```

`make docs-check` runs the site check offline. It is separate from
`make check`, which covers application and infrastructure validation. For the
difference, refer to
[Run local checks](content/en/docs/getting-started/local-checks.md). The
preview URL is `http://localhost:1313/cetirun/`.

The site check also checks the writing style of the pages. To list the style
findings of each page, run `python3 site/scripts/check_site.py --style-report`.
For the rule levels and sentence case, refer to
[Style checks](content/en/docs/contributing/site.md#style-checks).

Prerequisites: Make, Go 1.27.4 (the same version as the repository's
`.go-version`), C/C++ build tools for Hugo extended, Node.js 24 with npm, and
Python 3.11 or later. `HUGO_VERSION` in [`Makefile`](Makefile) pins Hugo
extended. [`go.mod`](go.mod) pins Docsy, and [`package.json`](package.json)
pins the npm packages. The generated `public/`, `resources/`, `.bin/`, and
`node_modules/` are ignored.

## Content map

| Repository entry | Site content |
| --- | --- |
| Root README | `content/en/docs/overview/`, `getting-started/` |
| `docs/agent-harness-contract.md` | `content/en/docs/reference/agent-harness.md` |
| `docs/agent-policy-admin.design.md` | `content/en/docs/reference/crds.md` |
| `docs/guides/tailscale.md` | `content/en/docs/guides/tailscale.md` |
| `docs/live-copilot-model-selection.design.md` | `content/en/docs/guides/models.md` |
| `app/cetirun-chart/README.md` | `content/en/docs/reference/helm.md` |
| `app/cetirun-operator/README.md` | `content/en/docs/reference/crds.md`, `helm.md` |
| `infra/README.md` | `content/en/docs/operations/infrastructure.md` |
| `infra/storage/README.md` | `content/en/docs/operations/storage.md` |
| `infra/istio/README.md` | `content/en/docs/operations/istio.md` |
| `infra/cert-manager/README.md` | `content/en/docs/operations/public-ingress.md` |
| `scripts/README.md` | `content/en/docs/reference/scripts.md` |

Compare the documented fields, defaults, and commands with the applicable
source each time that they change. Describe deployment prerequisites separately
from the capabilities that are enabled by default. Do not use a local render as
evidence that a live deployment works.

## Upstream licenses

The package managers keep the dependency sources and notices:

- Docsy: Apache-2.0, `LICENSE` in the pinned Hugo module.
- Bootstrap: MIT, `node_modules/bootstrap/LICENSE`.
- jQuery: MIT, `node_modules/jquery/LICENSE.txt`.
- Lunr: MIT, `node_modules/lunr/LICENSE`.
- Font Awesome Free: icons CC BY 4.0, fonts SIL OFL 1.1, code MIT,
  `node_modules/@fortawesome/fontawesome-free/LICENSE.txt`.

No reference-site product content, generated pages, binaries, or caches are
vendored. Keep dependency notices in any distribution of bundled assets.
