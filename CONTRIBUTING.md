# Contributing to CetiRun

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

Contributions can be code, tests, documentation, bug reports, or feedback about
how the runtime works for you.

## Before you start

- Read the [Code of Conduct](CODE_OF_CONDUCT.md).
- Before you open a bug report, feature request, or help request, search the
  [issues that already exist](https://github.com/Azure/cetirun/issues).
- For a change that is larger than a typo fix, open an issue to discuss the
  problem and the approach. If maintainers already agreed on the work, link to
  that discussion. Do not start another discussion.
- For the local setup and checks, follow [DEVELOPMENT.md](DEVELOPMENT.md).

Report suspected vulnerabilities privately through the Microsoft Security
Response Center (MSRC), as [SECURITY.md](SECURITY.md) describes. Do not put a
vulnerability report, exploit, or sensitive reproduction in a public issue or
pull request. For other questions, refer to [SUPPORT.md](SUPPORT.md).

## Contributor License Agreement

For most contributions, you must agree to a Microsoft Contributor License
Agreement (CLA). The CLA confirms that you have the right to grant permission
to use your contribution. Read the terms at the
[Microsoft CLA site](https://cla.opensource.microsoft.com/).

If an agreement is necessary, follow the instructions of the CLA bot on your
pull request. An agreement normally covers other repositories that use the
same Microsoft CLA process. If the check does not appear or is unclear, ask a
maintainer. If the check does not appear, do not treat this as an exemption.
Before they merge, maintainers confirm the applicable CLA requirements.

Keep the copyright and license notices that exist. For new dependencies or
copied code, identify the source and license. This lets reviewers make sure
that the dependency or copied code can be included under the project's
[MIT license](LICENSE).

## Keep contributions safe to publish

Issues, pull requests, attachments, screenshots, examples, and test fixtures must
be suitable for public access. Use synthetic data, example domains such as
`example.com`, and dummy values such as `sk-dummy`.

Do not include these items:

- Credentials, tokens, cookies, authorization headers, or kubeconfigs
- Private endpoints or registry addresses
- Tenant or subscription identifiers
- Customer data or private agent conversations
- Internal documents and logs

Do not upload a full diagnostic bundle. Sanitize the smallest relevant excerpt
instead. Examine the contents of images and also the text that accompanies
them.

Private deployment configuration and environment-specific state do not belong
in this repository. We recommend that a reproduction describes the behavior so
that access to a private environment is not necessary. If you cannot share it
safely, ask for a suitable channel to report it. When you ask, do not post the
sensitive material.

## Prepare a pull request

1. Work on a meaningful branch, for example `fix/portal-session-list`,
   `feat/instance-status`, or `docs/local-development`.
2. Keep the change focused. Keep unrelated work as it is. Coordinate changes
   that cross component ownership.
3. Where practical, add a regression test for changed behavior. When the
   behavior changes, update the documentation and examples that users see.
4. When necessary, regenerate checked-in artifacts from their source. Follow
   the [generated-file guidance](DEVELOPMENT.md#generated-files-and-dependencies).
5. Run the checks for the affected components. To find them, refer to
   [Checks for each change](DEVELOPMENT.md#checks-for-each-change). Record the
   exact commands, the results, and anything that you did not run. Include
   dependencies or environment access that you did not have.
6. Examine the full diff for unintended files and sensitive data.
7. Open a pull request against `main` with the template. Explain the problem,
   the approach, the related issue, and the compatibility or operational
   impact.

Use short, descriptive commit subjects and pull request titles. Explain the
reason for a change in plain language. Do not mix dependency upgrades or
format-only changes into an unrelated fix.

## Review and acceptance

Maintainers review scope, correctness, tests, compatibility, documentation, and
licenses. They also examine if the change is safe to publish.
[GOVERNANCE.md](GOVERNANCE.md) describes the project responsibilities. Ask the
maintainers to arrange a review for the affected area.

Before a merge, a maintainer other than the author must approve the change.
The applicable CLA requirements and required checks must be satisfied. Review
concerns must be resolved. For changes to authentication, isolation,
CRDs, storage, or shared component contracts, more reviewers can be necessary.

Maintainers can request a smaller change, defer work, or decline a proposal
with an explanation. An issue or pull request is not a promise of acceptance,
a response deadline, or a release date. Contributors are not expected to have
cloud credentials or publish release artifacts.
