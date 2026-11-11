---
title: "Contributing"
description: "Find the relevant source, propose a focused change, and validate it locally."
weight: 80
no_list: true
---

Contribute code, tests, documentation, bug reports, or feedback. For the rules
that apply to contributions, start with
[CONTRIBUTING.md]({{< repo "CONTRIBUTING.md" >}}) and the
[Code of Conduct]({{< repo "CODE_OF_CONDUCT.md" >}}).

- [Prepare a development environment](development/) — source layout and local setup
- [Run local checks](../getting-started/local-checks/) — component and documentation checks
- [Documentation](site/) — write, preview, and validate the site

Before you file an issue, search the issues that already exist. Before you
implement a change that is larger than a typo, discuss it. If maintainers
already agreed on the work, link that discussion. Do not reopen the decision.

For questions and non-security bug reports, follow
[SUPPORT.md]({{< repo "SUPPORT.md" >}}). Community support is best-effort, with
no promised response time, release schedule, long-term support window, or
backport. A repository issue is not an Azure support ticket or a commercial
support agreement.

## Find the component

| Change | Source |
| --- | --- |
| Instance reconciliation or API | [Operator]({{< repo "app/cetirun-operator" "tree" >}}) |
| Agent packaging or Helm settings | [Runtime chart]({{< repo "app/cetirun-chart" "tree" >}}) |
| Portal backend or frontend | [Portal]({{< repo "app/cetirun-portal" "tree" >}}) |
| Shared infrastructure | [Infrastructure]({{< repo "infra" "tree" >}}) |
| Reader documentation | [Site content]({{< repo "site/content/en/docs" "tree" >}}) |

Use [DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}) for local setup and validation.
For documentation, follow the [site guide](site/).
[GOVERNANCE.md]({{< repo "GOVERNANCE.md" >}}) describes project responsibilities.
The component map does not assign owners or create automatic code-owner review
routing. Ask maintainers to arrange review for the affected area.

## Make a focused change

1. Use a meaningful branch, such as `docs/local-development`. Keep the change
   focused. Do not change or remove unrelated work. Coordinate edits across
   components.
2. Explain the problem and the compatibility or operational effects. If it is
   practical, add a regression test. Update the documentation for changed
   behavior. Regenerate checked-in artifacts from source with the guidance in
   [DEVELOPMENT.md]({{< repo "DEVELOPMENT.md" >}}).
3. Run the checks for the affected components. Record the exact commands, the
   results, the dependencies that are not available, and the checks that you
   did not run. Network access can be necessary to install dependencies. Local
   checks do not authorize deployment, release, or live integration work.
4. Examine the full diff and its evidence for unintended files and sensitive
   data. Open a pull request against `main` with the repository template. Link
   the agreed issue or proposal.

Before you submit a change, run local tests. Cloud deployment, model-provider
access, and Tailscale integration must have separate tests in an authorized
environment. Security review stays separate from test results. Cloud
credentials and access to publish are not necessary for contributors.

## Keep reports and examples safe to publish

Use synthetic data, public example domains such as `example.com`, and dummy
values such as `sk-dummy`. Include the affected component, the commit or
artifact version, the expected and actual behavior, and a minimal shareable
reproduction. Examine screenshots as well as text.

Do not publish these items:

- Credentials, cookies, or authorization headers
- Kubeconfigs
- Private endpoints or registries
- Tenant or subscription identifiers
- Customer data
- Private agent conversations
- Internal documents and logs

Share only a short, sanitized excerpt that is relevant to the issue, not a full
diagnostic bundle. The [runtime privacy disclosure]({{< repo "PRIVACY.md" >}})
explains why session logs, browser state, and portal responses can contain
sensitive information.

Report suspected vulnerabilities privately through the Microsoft Security
Response Center (MSRC). Follow the instructions in
[SECURITY.md]({{< repo "SECURITY.md" >}}). Public issues and pull requests are
not channels for vulnerability reports, even for sanitized reproductions.
Use the [Code of Conduct]({{< repo "CODE_OF_CONDUCT.md" >}}) contact information
for conduct concerns.

## CLA and independent review

For most contributions, a Microsoft Contributor License Agreement (CLA) is
necessary. Follow the CLA bot's instructions and the terms that
[CONTRIBUTING.md]({{< repo "CONTRIBUTING.md" >}}) links to. If the check is
absent or unclear, ask a maintainer. An absent check is not an exemption.

Before merge, these conditions must be true:

- A maintainer other than the author approves the change.
- The applicable CLA conditions and the required checks are satisfied.
- The review concerns are resolved.

More reviewers can be necessary for authentication, isolation, CRDs, storage,
and shared contracts. Maintainers make sure that these conditions are met. This
includes the conditions that repository settings do not enforce. Maintainers
also decide if and when to accept and release a contribution.

## Licenses and third-party material

First-party CetiRun material is licensed under [LICENSE]({{< repo "LICENSE" >}})
(MIT). Keep the copyright and license notices that already exist. Identify the
source and license of new dependencies, copied code, and assets, so that
reviewers can examine them.

[NOTICE]({{< repo "NOTICE" >}}) separates adapted source from software that
image recipes install. It also records unresolved provenance and distribution
work. CetiRun source headers or image labels do not relicense bundled programs,
dependencies, artwork, or external services. The notice is not a complete
per-image inventory or confirmation that redistribution obligations are met.

`make license-check` covers supported source files and Makefiles across the
repository. These files include site HTML templates and Python helpers.
Markdown, YAML, lockfiles, artwork, and documented upstream/generated
exceptions are outside that mechanical check. They are not outside their
license obligations. Refer to the
[header procedure](../reference/scripts/#license-headers).

The Portal frontend build generates `THIRD_PARTY_LICENSES.txt` from its
production dependencies and embeds it with the UI. If a necessary notice is
absent, the build fails unless an exact, verified supplement is recorded. When
you update frontend dependencies, follow
[Portal notice generation](../reference/scripts/#portal-third-party-notices).
That artifact does not cover the documentation theme, and it does not cover
every runtime-image package.

The documentation site has its own third-party assets, which are separate from
the runtime images. Docsy, Bootstrap, jQuery, Lunr, and Font Awesome have their
own notices and license terms. For their license locations, refer to
[the site source guide]({{< repo "site/README.md" >}}). Keep the applicable
notices with distributed bundles. The original project logo is mounted into the
site. Do not use a source header as proof of artwork provenance or trademark
permission.
