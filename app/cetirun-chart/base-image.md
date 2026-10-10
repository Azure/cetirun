# Shared base image versions

`Dockerfile.base` supplies the tools used by both native agent images. Versions
below were checked against public upstream releases on 2026-10-08. Standalone
tool archives use version/commit and checksum pins; npm tools use explicit
release versions.

| Component | Selected version | Upstream source |
| --- | --- | --- |
| Azure Linux | 3.0, refreshed core image digest | [Releases](https://github.com/microsoft/azurelinux/releases) |
| Node.js | 24.21.0 LTS | [Release index](https://nodejs.org/dist/index.json) |
| npm | 12.2.0 | [Release notes](https://github.com/npm/cli/releases/tag/v12.2.0) |
| Microsoft Go builder | 1.27.2-1 | [Microsoft Go](https://github.com/microsoft/go) |
| Codex | 0.160.1 | [npm package](https://www.npmjs.com/package/@openai/codex/v/0.160.1) |
| Claude Code | 2.1.292 | [npm package](https://www.npmjs.com/package/@anthropic-ai/claude-code/v/2.1.292) |
| OpenCode | 1.18.35 | [npm package](https://www.npmjs.com/package/opencode-ai/v/1.18.35) |
| GitHub CLI | 2.102.0 | [Release](https://github.com/cli/cli/releases/tag/v2.102.0) |
| kubectl | 1.37.1 | [Release](https://github.com/kubernetes/kubernetes/releases/tag/v1.37.1) |
| Tailscale | 1.104.1 | [Release](https://github.com/tailscale/tailscale/releases/tag/v1.104.1) |
| ripgrep | 15.2.0 | [Release](https://github.com/BurntSushi/ripgrep/releases/tag/15.2.0) |
| uv / uvx | 0.12.23 | [Release](https://github.com/astral-sh/uv/releases/tag/0.12.23) |
| ttyd | 1.7.7, unchanged | [Latest stable release](https://github.com/tsl0922/ttyd/releases/tag/1.7.7) |
| Supervisor | 4.3.0, now explicitly pinned | [PyPI](https://pypi.org/project/supervisor/4.3.0/) |
| pip-vendored msgpack | 1.2.3 | [PyPI](https://pypi.org/project/msgpack/1.2.3/) |
| pip-vendored setuptools | 81.0.0, compatibility ceiling | [Removal of pkg_resources in 82.0.0](https://setuptools.pypa.io/en/latest/history.html#v82-0-0) |
| Oh My Zsh | `60c9a7a839b790cd905d0fd4419435124fd1bdc0` | [Upstream commit](https://github.com/ohmyzsh/ohmyzsh/commit/60c9a7a839b790cd905d0fd4419435124fd1bdc0) |

## Compatibility choices

Node stays on the existing LTS line. Node 26.11.1 is the newer Current release,
not yet LTS at the review date. The pinned OpenClaw release accepts Node 24.21.0.

System packages, including Python, pip, Git, and Azure CLI, follow the supported
Azure Linux 3.0 repositories through `tdnf install` and `tdnf update`. They are
not replaced with unrelated upstream major versions. Azure CLI 2.91.0 is both
the current upstream release and the version available from the distribution.
System pip and Azure CLI's private pip still require `pkg_resources`, so their
setuptools replacement stays at the last compatible release, 81.0.0, rather
than 84.0.0. Both vendor inventories and SBOM references are checked after the
payloads are updated.

GitHub CLI and Tailscale now require Go 1.27. All shared Go builders, module
minimums, and `.go-version` move together. Microsoft Go 1.27 supports cgo-less
OpenSSL automatically; the removed `ms_nocgo_opensslcrypto` experiment must not
be passed to it. Tailscale archive builds explicitly stamp their release
version, and kubectl's major/minor metadata is derived from its selected version.
The [kubectl skew policy](https://kubernetes.io/releases/version-skew-policy/#kubectl)
requires a server within one minor version of the client; qualify the target
cluster separately before deploying this image. In particular, the legacy
`infra/cluster/main.bicep` default is still Kubernetes 1.34, outside the supported
range for this client. This image update does not change that infrastructure
default or establish compatibility with a 1.34 cluster.

npm 12 supplies its own fixed undici dependency. The old in-place npm patch is
removed. CLI installs save exact versions so later agent-layer installs cannot
change them through a caret range. Only the reviewed OpenCode, Claude Code, and
OpenClaw lifecycle scripts are approved, using `npm install-scripts approve`.
Registry overrides and the existing `min-release-age=1` setting remain in
effect. npm measures this setting in days, so tool pins must have been published
for at least 24 hours. Codex 0.161.0 and Claude Code 2.1.293 were inside that
window when checked; the selected releases are the latest stable versions
eligible under this policy at the review time.
The Pi example still fixes undici 6.26.0 when using its older published base,
but leaves the already-fixed 6.27.0 and 6.28.0 payloads untouched. Its SDK pin
is unchanged.

Oh My Zsh has no stable-release version channel; the selected upstream commit
and archive checksum provide its immutable snapshot. uv and uvx now live in
`/usr/local/bin` so a persistent HOME cannot hide the image's updated tools.
Existing HOME files, including older user-local copies, are not deleted.
No shared HOME contents, credentials, agent provider settings, or permission
defaults are migrated by this update. The separate desktop image and native
agent releases are outside this base-tool update.
