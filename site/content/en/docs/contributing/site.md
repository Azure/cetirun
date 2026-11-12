---
title: "Contribute to documentation"
linkTitle: "Documentation"
description: "Write, preview, and validate CetiRun documentation."
weight: 10
---

## Toolchain

The site pins **Hugo extended 0.164.0**, **Docsy 0.16.0**, Bootstrap 5.3.8,
and Font Awesome 6.7.2. Use these tools:

- Go 1.27.4 to install the pinned Hugo binary. This version matches the
  repository's [`.go-version`]({{< repo ".go-version" >}}).
- A C/C++ compiler for the extended Hugo build
- Node.js 24 with npm
- Python 3.11 or later
- Make

When you change the toolchain, refer to the official
[Hugo installation instructions](https://gohugo.io/installation/) and the
[Docsy module guide](https://www.docsy.dev/docs/get-started/docsy-as-module/).

## Check and preview

Run these commands from the repository root:

```bash
make -C site check
make -C site serve
```

Open `http://localhost:1313/cetirun/`. By default, the server binds to
loopback. If that port is not available, use `PORT=1314`.

If you use a Hugo binary that already exists and matches the pinned version, it
is not necessary to build another one:

```bash
make -C site check HUGO=/absolute/path/to/hugo
make -C site serve HUGO=/absolute/path/to/hugo PORT=1314
```

The first run downloads Go/Hugo modules and npm dependencies. After that run,
the validator tests are offline:

```bash
make -C site test
```

To separate dependency setup from the repository's offline documentation gate,
run these commands:

```bash
make -C site deps
make docs-check
```

If you use a Hugo binary that already exists, pass
`HUGO=/absolute/path/to/hugo` to both commands. `make check` does not include
documentation. Run `make docs-check` separately.

The root gate disables automatic Go toolchain/module downloads and sets npm to
offline mode. As a result, you must install the dependencies first. Keep
`module.proxy = ""` in `hugo.toml`. With this value, Hugo inherits the gate's
`GOPROXY=off` setting and does not replace it with its default `direct` mode.

`check` runs those tests and the repository-wide header check. It builds the
site and then validates these items:

- Internal URLs and fragments
- Repository Markdown entry points and heading anchors
- Navigation
- The `docs/` inventory
- Search index coverage
- Go-version consistency
- Logo integrity
- Basic accessibility

It does not run the deployment examples. Static checks do not replace
keyboard, screen-reader, mobile, or browser visual review.

By default, the Hugo cache is in `$XDG_CACHE_HOME/cetirun/hugo` or
`$HOME/.cache/cetirun/hugo`, outside the worktree. `.bin`, `node_modules`,
`public`, and `resources` are ignored build products. Do not commit them.

## Edit content

Add Markdown under `site/content/en/docs` with a title, a description, and a
numeric weight. Use a section `_index.md` to give a reader an entry point. Keep
relative links valid from the rendered URL, with its trailing slash.

Use the `repo` shortcode for links to source paths. The validator makes sure
that the target exists in the checkout. Keep descriptions specific enough to
work in navigation and search.

When you add or move a file under `docs/`, update the checker's `POINTERS` map
or `DOC_ASSETS` classification. When you replace a document with a pointer,
keep the inbound repository links and their heading anchors.

Keep future work on one short Roadmap page. Do not add detailed plans or
unimplemented usage instructions to other documentation. Keep current usage
and its limitations accurate until the implementation changes.

Assign every page to one module in `site/data/navigation.toml`: Overview,
User Guide, Operations, Reference, or Contributing. Navigation is scoped to
that module on both the landing page and its child pages. Source paths can
stay unchanged. The checks reject pages without an assignment, duplicates, and
cross-module sidebar entries.

Put user tasks under User Guide. Operations groups platform tasks into Install,
Configuration, and Troubleshooting. Link to Reference for field and parameter
definitions. Do not repeat these definitions.

### Writing style

- Name concepts and reference pages with short noun phrases, such as
  **Architecture** and **Security**. Name tasks with actions, such as
  **Choose a model** or **Manage an instance**.
- Use present tense and active voice. Describe what the product does. Do not
  narrate development work or compare the product with an unfinished design.
- Start with the reader's task or the component's purpose. Avoid introductions
  that only announce what the page describes.
- Explain prerequisites and limitations at the point where they are necessary
  for the reader. State the consequence and the necessary action. Use warnings
  for security or data-loss risks.
- Keep API fields, commands, defaults, and identifiers accurate. An edit to the
  prose must not change their meaning or imply that a defect is fixed.
- Keep future work on Roadmap. When a compatibility difference is important,
  use a specific version.

Use the [Kubernetes documentation style guide](https://kubernetes.io/docs/contribute/style/style-guide/)
for technical text. Use Google's
[timeless documentation guidance](https://developers.google.com/style/timeless-documentation)
for release-independent text.

Distinguish local rendering, connected read-only inspection, and cloud changes.
Examples must use placeholders and must not invent a release registry,
supported feature, or successful integration test.

The original `docs/logo.jpg` is mounted into the generated site. Do not resize,
re-encode, or replace that source image.

## Publication

The base URL is configured for a possible `/cetirun/` project site, so that
local validation finds subpath mistakes. A base URL is not a deployment. This
repository change supplies only pull-request validation and local preview.
There is no Pages deployment workflow, deployment permission, or enabled
publication step.

The `CetiRun docs checks` workflow runs for changes to the site, its source
components, repository scripts, Go-version file, Makefile, or linked policies.
It uses the repository Go version and the site's dependency locks. The workflow
does not make its check a repository-required gate. That is a maintainer
setting outside the site sources.

## Theme and third-party assets

Docsy supplies the documentation sidebar, table of contents, breadcrumbs,
theme selection, and client-side search. CetiRun supplies its own home layout,
styles, and local-asset head. jQuery and Lunr are bundled from pinned npm
dependencies. The site configures no analytics service.

Keep upstream license notices with dependencies. For dependency license
locations and the content map, refer to
[the site source guide]({{< repo "site/README.md" >}}). These statements are
about the documentation site, not about telemetry in the CetiRun runtime or how
the runtime handles data.
