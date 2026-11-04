# Maintaining the Hermes runtime

CetiRun serves the Hermes browser workspace from the gateway's Webapp listener.
The same gateway owns API requests, message-channel sessions, queues, and the
workspace. There is one supervised gateway, plus the separate ttyd and Tailscale
services. Closing a browser does not stop the gateway. Restarting the gateway
ends its host-terminal shells; persistent history does not make in-flight tools
or model requests resumable.

## Pinned build

| Input | Value |
| --- | --- |
| Hermes release | `v0.22.7` |
| Source commit | `b200f5bc60e31588ba2cb60e288029c4a54ba5fa` |
| Commit archive SHA-256 | `1f7d43fa1a515283929a739c42e10bf8d48910e523eccec74b2a2bece858d2b5` |
| Python | PM `3.14.9+20261021` |
| Node / npm | PM `26.12.1` / `12.0.2` |
| uv | PM `0.13.5` |
| SQLite | `3.53.4`, tested by the Hermes interpreter |
| CPython SQLite extension source | `Python-3.14.9.tar.xz`, SHA-256 `e02d9ac21cebda6e46435369744a92c65cc7cd8669697694122cf97ffacd1e53` |

The [Dockerfile](Dockerfile) downloads the exact commit archive and checks its
hash. [PM build helper](pm/build.py) consumes that source's `pm/lock.json` and
uses upstream PM to verify tool downloads, build the application `.venv`, record
the `all`, `messaging`, `anthropic`, and `azure-identity` selections, and package
an independent manager runtime. The tools live in the Hermes image directory.
The image's PATH selects PM's `python3`, `node` and `npm` before the base tools;
`hermes-python` additionally activates Hermes' selected Python dependencies.

`NPM_REGISTRY` applies to the npm tool download and JavaScript dependencies.
`PYPI_INDEX_URL` selects the Python package index. Python, uv and Node binary
downloads use the URLs in the upstream PM lock and its checksum-addressed mirror.

The PM Python embeds SQLite 3.53.1. The image compiles the matching CPython
3.14.9 SQLite extension against the pinned 3.53.4 library, then loads it before
Hermes imports SQLite. It keeps the PM interpreter artifact unchanged. Both the
PM entry point and the base environment source binding install this extension;
a missing or mismatched extension fails qualification and startup.

The package metadata intentionally reports `0.0.0`. The release identity comes
from upstream `scripts/write_install_stamp.py`, including the exact release,
commit, Docker distribution and external update ownership. Runtime checks use
`hermes_cli.version_info`, the image provenance, and the PM tool facts.
`hermes update` does not upgrade the CetiRun image.

The image builds `apps/desktop/dist-webapp` with upstream
`build-webapp.mjs --deps-only`, followed by `--no-install`, and builds the TUI
with `scripts/build/tui.mjs`. It retains product receipts and the upstream agent
manifest. Browser startup does not install npm packages or download Electron.
The Chrome/CDP sidecar remains available for agent browser tools.

## Dependency lifecycle

`HERMES_PYTHON=/usr/local/bin/hermes-python` is the supported Python entry point
for the gateway, CLI, administrative helpers and native probes. It checks the
PM selection before activating dependencies and imports application code only
after that check. The image `.venv` is a build artifact, not a bypass for a
persisted plugin selection.

A fresh HOME uses the image's prepared environment. An explicit plugin install
uses upstream PM to create a generation below
`$HERMES_HOME/installs/<install-key>/environments`; the accompanying `facts.json`
is PM's selection record. The manifest keeps tool and core source writes out
of the immutable image. Platform extras remain required after plugin changes.
Desktop plugin files live under the persistent Hermes root's `desktop-plugins`.
Browser-compatible Desktop plugins can run without Electron; native windows,
global shortcuts, updater actions and unsupported file APIs retain upstream's
unavailable behavior.

Normal startup, gateway restart, Pod recreation and suspend/resume only validate
and activate prepared dependencies. They never call `refresh_dependencies()`
or rebuild a missing environment. A stale, incomplete or missing generation
stops startup without changing the selection or falling back to the base.

An image upgrade with extra plugin dependencies requires a separate preparation
step against the candidate image and the instance's retained HOME. With that
target and any downloads explicitly authorized, run
`hermes-prepare-dependencies` in the candidate environment before starting the
new gateway. The command asks upstream PM to preserve the recorded plugin and
extra selection while adding the required platform extras, publishes a verified
generation, and validates startup imports. A failed PM build leaves the previous
selection available for diagnosis. Startup after successful preparation must
also pass with external network access disabled. Back up the instance before
upgrading its runtime or database; do not run an old image on a migrated database.

The image applies a source-hash guarded change to the pinned PM candidate builder:
its existing startup import check runs before every generation publication, not
only dependency repair. This keeps a plugin with broken imports from replacing
a working selection. PM still owns building, validation and atomic publication.

## Platform-owned settings and access

The platform manages the API port (default `8642`), Webapp port `9120`, the public URL,
listener addresses and browser authentication on every supervised gateway start.
The Gateway model and provider settings are reapplied from the generated
configuration before each start. Persistent default-model edits in the Webapp
or CLI are reset to the platform selection; change `spec.model` through CetiRun
to retain a different platform model. Message channels, plugins, sessions and
unrelated preferences remain in HOME. Invalid managed input causes
startup failure. User profiles, `.env` files and alternate configuration loaders
cannot override these platform settings. External secret sources may populate
unrelated integration credentials through the pinned OnePassword source's
explicit environment mappings. Reserved variable mappings and bulk sources with
undeclared output names are rejected before hydration; this also applies to
profile aliases and the AI Gateway key.

The browser username is `cetirun`; the password is the existing `API_SERVER_KEY`.
`CETIRUN_AUTH_KEY` is the independent session-signing key. Both keys must exist
and are reused across normal restarts. The upstream browser credential at
`$HERMES_HOME/webapp/session-token` also persists, with directory mode `0700`
and file mode `0600`. Browser cookies still expire or can be signed out; the
persistent password is not an unlimited browser session. Never print these
values in logs, commands, URLs or test results.

The Operator routes `/ui/<instance>/`, removes the prefix when forwarding, and
sets `X-Forwarded-Prefix`. `webapp.public_url` carries the full HTTPS instance
URL. Native Webapp prefix handling owns assets, auth, files and sockets; there
is no Dashboard prefix rewrite middleware. Cookie paths, storage, WebSocket
tickets and plugin/file URLs must be qualified using two instances on one host.

Before building the Webapp, `compat/apply_webapp_storage.py` verifies the pinned
renderer entry and native window handoff source hashes, and installs browser
state scoping before any preference stores load. Local storage, session storage, storage events and
BroadcastChannel names are scoped to `/ui/<instance>`. Clearing one instance's
storage leaves the others intact. Existing unscoped browser keys are not
migrated because their owning instance is unknown. Only a native launch grant
already bound to the current origin and instance is carried into that instance's
session storage; child-window authorization uses the same scoped channel.
Native Desktop keeps its
existing storage behavior. The patch changes source modules, without rewriting
served HTML or JavaScript.

In managed ingress mTLS mode, both application listeners bind to loopback.
External TLS `15443` forwards API requests to the configured API port; TLS `15444` forwards Webapp
requests to `9120`. The target registration must explicitly approve the new
Webapp port. An old approval for `9119` does not authorize `9120`.

Readiness includes API health, gateway control-socket Webapp state, the password
auth provider and prebuilt products. Both readiness and liveness also verify
managed ingress certificates against Envoy's active certificate and check the
managed egress proxy when those helpers are enabled. Liveness otherwise uses
API `/health`, so a browser-side failure does not restart a gateway executing
other work.
Missing bundles, port conflicts, missing signing keys and invalid overlays must
never report Webapp Ready or select another listener or authentication mode.

## Bounded long-running tools

Hermes defaults to a 420-second outer tool deadline. Increasing a terminal
tool's requested timeout alone does not increase that deadline. For longer
work, merge finite limits into the existing native configuration:

```yaml
timeouts:
  tools:
    concurrent_batch: 600
    sequential_call: 600
```

Keep the overall run budget larger than the tool deadline and limit turns:

```bash
hermes chat --oneshot -Q --max-turns 8 --run-budget 660 --query-file -
```

CLI requests execute through the resident gateway, so a timeout environment
variable set only on the CLI process does not configure gateway execution.
Apply the native settings to the gateway without replacing unrelated retained
configuration. A child process writing output after a tool timeout does not
mean the foreground tool completed successfully.

## Sessions and integrations

A browser can resume an eligible gateway message-channel session. A reply to a
Discord-origin session retains its original delivery source; opening it in the
browser does not create a private branch. Old history without an executable
source binding remains history. The UI's source and takeover state must stay
visible when validating this behavior.

CetiRun keeps AI Gateway routing, model selection, workload-authenticated
Connections MCP and the platform Memory plugin. Memory authorization follows
the trusted session source and parent relationship: a Webapp owner-private
session can use private Memory, while a message-channel or subagent session
does not become private merely because the browser opened it. Plugin operations
must preserve the platform-managed integrations.

With Memory enabled, Hermes mounts platform instructions read-only at
`$HOME/.agents/AGENTS.md` and keeps personal files and skills elsewhere in
`$HOME/.agents` writable. Shared Skills stay on their direct read-only PVC mount
at `skills.mountPath`, so published updates do not require a Pod restart.
An explicit shared mount at `$HOME/.agents/skills` still occupies that directory.
Ordinary Hermes context volumes have a `40Mi` limit; protected reconciled
workloads retain their configured scratch limit.

Hermes discovers personal, configured and shared skills alongside the installed
Memory assets. The source-hash guarded `configs/apply-memory-skills.py` patch keeps
the reserved `cetirun-memory` skill bound to
`/opt/cetirun-memory/skills/cetirun-memory/SKILL.md` in both the native catalog and
direct skill lookup. User or shared copies cannot replace it by directory name,
flat filename or frontmatter name. Ordinary skills keep native discovery and
precedence; a symlink to the canonical read-only skill remains valid.
When Memory is enabled, native prompt construction reads skill metadata from the
files and does not read or write persisted skill snapshots. Disabling Memory
restores native snapshot behavior without replacing an existing snapshot.

## Validation and upgrades

Update the release, commit and checksum together, then check the new PM lock,
Webapp/TUI builders, native configuration and plugin contracts. Synchronize the
Hermes profile and Memory host pins. A CetiRun release change is separate and
must use `make set-version VERSION=X.Y.Z` when requested.

Upgrade each Hermes Agent to the matching runtime Chart and image when updating
the Operator. An Agent still pinned to an older Hermes profile is marked
`Failed` with `AgentConfigInvalid`; that preparation error does not replace its
existing workload or route. Portal Restart is unavailable until the Agent is
Ready again with the matching runtime. Other lifecycle or policy changes can
still stop a workload according to their normal rules.

The new runtime does not load packages from the old `lazy-packages` directory.
Install required optional dependencies through the current PM workflow; existing
files in that directory are retained. Memory playbooks qualified against the
old native version report `CompatibilityChanged` and require qualification for
the new version.

Run the repository's offline validation entry points:

```bash
python3 -B -m unittest scripts.tests.test_cetirun_image_dependencies scripts.tests.test_hermes_pm_runtime -v
make check
make docs-check
```

The image build runs `pm/verify.py` to check Python, Node/npm, Webapp/TUI assets,
time zones and SQLite WAL/FTS5. Unit tests cover environment selection, startup
configuration, authentication keys, routing and retained state. Memory and MCP
tests exercise the changed native adapter interfaces.

The focused browser storage test and existing native ingress test are described
in [DEVELOPMENT.md](../../../../DEVELOPMENT.md#runtime-chart-and-python-helpers).
They use synthetic data. Report offline validation and live deployment results
separately; local checks do not establish public login or model access.
