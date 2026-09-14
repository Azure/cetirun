# Contributing to CetiRun

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

Contributions can be code, tests, documentation, bug reports, or feedback on how
the runtime works for you.

## Before you start

- Read the [Code of Conduct](CODE_OF_CONDUCT.md).
- Search [existing issues](https://github.com/Azure/cetirun/issues) before opening
  a bug report, feature request, or help request.
- For changes beyond a typo, open an issue to discuss the problem and approach.
  If maintainers have already agreed on the work, link that discussion rather
  than starting another one.
- Follow [DEVELOPMENT.md](DEVELOPMENT.md) for local setup and checks.

Report suspected vulnerabilities privately through the Microsoft Security
Response Center (MSRC), following [SECURITY.md](SECURITY.md). Do not put a
vulnerability report, exploit, or sensitive reproduction in a public issue or
pull request. For other questions, see [SUPPORT.md](SUPPORT.md).

## Contributor License Agreement

Most contributions require a Microsoft Contributor License Agreement (CLA).
It confirms that you have the right to grant permission to use your contribution.
Read the terms at the [Microsoft CLA site](https://cla.opensource.microsoft.com/).

Follow the CLA bot's instructions on your pull request if an agreement is
required. An agreement normally covers other repositories using the same
Microsoft CLA process. Ask a maintainer if the check is missing or unclear;
do not treat a missing check as an exemption. Maintainers confirm the applicable
CLA requirements before merging.

Keep existing copyright and license notices. For new dependencies or copied
code, identify the source and license so reviewers can check that it can be
included under the project's [MIT license](LICENSE).

## Keep contributions safe to publish

Issues, pull requests, attachments, screenshots, examples, and test fixtures must
be suitable for public access. Use synthetic data, example domains such as
`example.com`, and dummy values such as `sk-dummy`.

Do not include credentials, tokens, cookies, authorization headers, kubeconfigs,
private endpoints or registry addresses, tenant or subscription identifiers,
customer data, private agent conversations, or internal documents and logs.
Sanitize the smallest relevant excerpt instead of uploading a full diagnostic
bundle. Check image contents as well as accompanying text.

Private deployment configuration and environment-specific state do not belong
in this repository. A reproduction should describe the behavior without
requiring access to a private environment. If you cannot share it safely, ask
for an appropriate reporting channel without posting the sensitive material.

## Prepare a pull request

1. Work on a meaningful branch, such as `fix/portal-session-list`,
   `feat/instance-status`, or `docs/local-development`.
2. Keep the change focused. Preserve unrelated work and coordinate changes that
   cross component ownership.
3. Add a regression test for changed behavior where practical. Update user-facing
   documentation and examples when the behavior changes.
4. Regenerate checked-in artifacts from their source when needed; follow the
   [generated-file guidance](DEVELOPMENT.md#generated-files-and-dependencies).
5. Run the checks for the affected components. Record exact commands, results,
   and anything not run, including missing dependencies or environment access.
6. Inspect the entire diff for unintended files and sensitive data.
7. Open a pull request against `main` using the template. Explain the problem,
   approach, related issue, and compatibility or operational impact.

Use short, descriptive commit subjects and pull request titles. Explain the
reason for a change in plain language. Do not mix dependency upgrades or
format-only changes into an unrelated fix.

## Review and acceptance

Maintainers review scope, correctness, tests, compatibility, documentation,
licensing, and whether the change is safe to publish. Project responsibilities
are described in [GOVERNANCE.md](GOVERNANCE.md). Ask the maintainers to arrange
review for the affected area.

A maintainer other than the author must approve a change before merge.
Applicable CLA requirements and required checks must be satisfied, and review
concerns must be resolved. Changes to authentication, isolation, CRDs, storage,
or shared component contracts may need additional reviewers.

Maintainers may request a smaller change, defer work, or decline a proposal
with an explanation. Opening an issue or pull request does not promise
acceptance, a response deadline, or a release date. Contributors are not
expected to have cloud credentials or publish release artifacts.
