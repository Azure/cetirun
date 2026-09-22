---
title: "Contribute to documentation"
linkTitle: "Documentation"
description: "Write, preview, and validate CetiRun documentation."
weight: 10
---

## Toolchain

The site pins **Hugo extended 0.164.0**, **Docsy 0.16.0**, Bootstrap 5.3.8,
and Font Awesome 6.7.2. Use Go 1.26.8, matching the repository's
[`.go-version`]({{< repo ".go-version" >}}), to install the pinned Hugo
binary, a C/C++ compiler for its extended build, Node.js 24 with npm,
Python 3.11 or later, and Make.

Consult the official
[Hugo installation instructions](https://gohugo.io/installation/) and
[Docsy module guide](https://www.docsy.dev/docs/get-started/docsy-as-module/)
when changing the toolchain.

## Check and preview

From the repository root:

```bash
make -C site check
make -C site serve
```

Open `http://localhost:1313/cetirun/`. The server binds to loopback by default.
Use `PORT=1314` if that port is occupied.

An existing matching Hugo binary avoids building another one:

```bash
make -C site check HUGO=/absolute/path/to/hugo
make -C site serve HUGO=/absolute/path/to/hugo PORT=1314
```

The first run downloads Go/Hugo modules and npm dependencies. Subsequent
validator tests are offline:

```bash
make -C site test
```

To separate dependency setup from the repository's offline documentation gate:

```bash
make -C site deps
make docs-check
```

If you use an existing Hugo binary, pass `HUGO=/absolute/path/to/hugo` to both
commands. `make check` does not include documentation; run `make docs-check`
separately. The root gate disables automatic Go toolchain/module downloads and
sets npm to offline mode, so missing dependencies must be installed first.
Keep `module.proxy = ""` in `hugo.toml` so that Hugo inherits the gate's
`GOPROXY=off` setting instead of replacing it with its default `direct` mode.

`check` runs those tests and the repository-wide header check, builds the site,
then validates internal URLs and fragments, repository Markdown entry points
and heading anchors, navigation, the `docs/` inventory, search index coverage,
Go-version consistency, logo integrity, and basic accessibility.
It does not execute the deployment examples. Static checks do not
replace keyboard, screen-reader, mobile, or browser visual review.

Hugo cache defaults to `$XDG_CACHE_HOME/cetirun/hugo` or
`$HOME/.cache/cetirun/hugo`, outside the worktree. `.bin`, `node_modules`,
`public`, and `resources` are ignored build products. Do not commit them.

## Edit content

Add Markdown under `site/content/en/docs` with a title, description, and numeric
weight. Use a section `_index.md` to give a reader an entry point. Keep relative
links valid from the rendered URL, including its trailing slash.

Use the `repo` shortcode for links to source paths; the validator verifies that
the target exists in the checkout. Keep descriptions specific enough to work
in navigation and search.

When adding or moving a file under `docs/`, update the checker's `POINTERS` map
or `DOC_ASSETS` classification. Preserve inbound repository links, including
heading anchors, when replacing a document with a pointer.

Keep future work on one short Roadmap page. Do not add detailed plans or
unimplemented usage instructions to other documentation. Keep current usage
and its limitations accurate until the implementation changes.

Assign every page to one module in `site/data/navigation.toml`: Overview,
User Guide, Operations, Reference, or Contributing. Navigation is scoped to that module on both the
landing page and its child pages. Source paths may stay unchanged; the checks
reject missing assignments, duplicates, and cross-module sidebar entries.

Put user tasks under User Guide. Operations groups platform tasks into Install,
Configuration, and Troubleshooting; link to Reference for field and parameter
definitions instead of repeating them.

### Writing style

- Name concepts and reference pages with short noun phrases, such as
  **Architecture** and **Security**. Name tasks with actions, such as
  **Choose a model** or **Connect Microsoft Teams**.
- Use present tense and active voice. Describe what the product does without
  narrating development work or comparing it with an unfinished design.
- Start with the reader's task or the component's purpose. Avoid introductions
  that only announce what the page describes.
- Explain prerequisites and limitations where the reader needs them. State the
  consequence and the required action; use warnings for security or data-loss
  risks.
- Keep API fields, commands, defaults, and identifiers accurate. Editing the
  prose must not change their meaning or imply that a defect has been fixed.
- Keep future work on Roadmap. Use a specific version when a compatibility
  difference matters.

Use the [Kubernetes documentation style guide](https://kubernetes.io/docs/contribute/style/style-guide/)
for technical writing and Google's
[timeless documentation guidance](https://developers.google.com/style/timeless-documentation)
for release-independent wording.

Distinguish local rendering, connected read-only inspection, and cloud changes.
Examples must use placeholders and must not invent a release registry,
supported feature, or successful integration test.

The original `docs/logo.jpg` is mounted into the generated site. Do not resize,
re-encode, or replace that source image.

## Publication

The base URL is configured for a possible `/cetirun/` project site so local
validation catches subpath mistakes. A base URL is not a deployment.
This repository change provides pull-request validation and local preview only:
there is no Pages deployment workflow, deployment permission, or enabled
publication step.

The `CetiRun docs checks` workflow runs for changes to the site, its source
components, repository scripts, Go-version file, Makefile, or linked policies.
It uses the repository Go version and the site's dependency locks. The workflow
does not turn its check into a repository-required gate; that is a maintainer
setting outside the site sources.

## Theme and third-party assets

Docsy supplies the documentation sidebar, table of contents, breadcrumbs,
theme switching, and client-side search. CetiRun supplies its own home layout,
styles, and local-asset head. jQuery and Lunr are bundled from pinned npm
dependencies; no analytics service is configured by the site.

Keep upstream license notices with dependencies. See
[the site source guide]({{< repo "site/README.md" >}}) for dependency license
locations and the content map. These statements concern the documentation
site, not telemetry or data handling in the CetiRun runtime.
