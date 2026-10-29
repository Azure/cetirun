# Maintaining the Hermes runtime

CetiRun builds Hermes from an immutable upstream source release. The Dockerfile
downloads the selected GitHub tag, verifies the archive checksum, checks the
package version in `pyproject.toml`, and installs the repository's checked-in
`uv.lock`. It then builds the upstream Dashboard and TUI assets.

The reference for this release is the
[upstream v2026.9.24 Dockerfile](https://github.com/NousResearch/hermes-agent/blob/v2026.9.24/Dockerfile)
and [installer](https://github.com/NousResearch/hermes-agent/blob/v2026.9.24/scripts/install.sh).
CetiRun follows the container's `uv sync --frozen` path with the `all`,
`anthropic`, and `azure-identity` extras. It uses the upstream npm workspace
installation and Dashboard/TUI build commands. It does not run the interactive
host installer or replace CetiRun's HOME and process supervision.

CetiRun skips the upstream Playwright browser download because the chart
provides a separate Chrome/CDP desktop sidecar. UI assets build on the builder's
native architecture using the upstream-pinned Node 26 image; only their
architecture-independent output enters the target image. Python, SQLite, and
the final image build for `linux/amd64`. The runtime retains the shared base's
Node 24.21.0, which satisfies the upstream package's Node engine range.

## Compatibility cleanup in 0.21.5

| Previous integration | Replacement |
| --- | --- |
| Checksum-verified PyPI wheel, `--no-deps` install, then a second `[all]` install | One source release and its checked-in `uv.lock` |
| Separate Azure Identity and PyJWT version overrides | Dependencies from the upstream lock and `azure-identity` extra |
| Copilot helper importing private Python functions | Public `hermes model` and `hermes auth status copilot` commands |
| Import-time Teams registration and old adapter signatures | Plugin-context registration, scoped settings, and current reconnect/metadata arguments |
| Gateway shell pipeline and Dashboard `--insecure` | Supervisor-owned gateway process and the upstream Dashboard password provider |

The shared base's unrelated dependency fixes remain unchanged. The pinned
SQLite build is retained because the base's SQLite does not meet this release's
WAL safety check. Its compiler, linker, C headers, Linux kernel headers, and
Make stay in a separate build stage; the runtime receives only the library.

Teams discovery registers lightweight factories. The adapter and Azure SDKs
load when Hermes creates the adapter or sends a standalone message, not during
the time-limited plugin discovery step. The upstream discovery timeout is
unchanged.

## CetiRun-owned integration

CetiRun keeps these layers on top of the upstream source:

- the persistent user and HOME contract at UID/GID `1024` and
  `/home/vibe/.hermes`;
- the `cetirun_teams` platform plugin and Azure Workload Identity support;
- the in-pod CetiRun authentication service, which the entrypoint does not run
  when `CETIRUN_AI_GATEWAY=true`: on the platform AI Gateway the owner signs
  in to GitHub Copilot through the Portal;
- Supervisor wiring for the Hermes gateway, Dashboard, terminal, and Tailscale;
- the external shared-skills directory and CetiRun-owned persona/context files;
- the SQLite version and compile flags used by the upstream container to avoid
  the WAL-reset corruption bug on older distribution packages;
- Dashboard password authentication. The username is `cetirun`; the existing
  `API_SERVER_KEY` is the password, and `CETIRUN_AUTH_KEY` signs Dashboard
  sessions.

The Chart supplies the Dashboard settings explicitly. For older Charts, the
entrypoint supplies the same defaults from the existing keys without
overriding explicit settings. Missing keys do not disable upstream
authentication.

Older Charts also retain a `copilot-auth` wrapper that calls
`hermes-copilot-auth.py`. That path remains in the image as a CLI-only
compatibility entrypoint; it no longer imports private Hermes auth functions.

Do not replace these contracts with the upstream container layout without also
qualifying persistent HOME ownership, chart probes and ports, Istio routes,
Portal authentication flows, Teams delivery, and upgrade behavior.

## Instance URL prefixes

The Operator's Dashboard route owns `/ui/<instance>/`. It strips that prefix
before forwarding a request and supplies it in `X-Forwarded-Prefix`. The
Dashboard must use that value for browser requests, resources, and redirects.
Ingress cannot recover the intended instance once a browser sends an
unprefixed `/auth/...` request.

The Operator also supplies `HERMES_DASHBOARD_PUBLIC_URL` with the same HTTPS
origin and `/ui/<instance>` prefix. This declares the public hostname to the
Dashboard's Host and WebSocket Origin checks when managed ingress mTLS binds
the backend to loopback. It does not disable those checks or password
authentication. Under mTLS, retained HOME `.env` assignments cannot override
this Pod-owned value, including after a supervised restart.

Two upstream paths need adaptation in 0.21.5: the standalone login page
submits to `/auth/password-login`, and the default Vite build loads Chat's
lazy JavaScript and CSS from `/assets/`. Both omit the instance prefix.
Rewriting `fetch` alone cannot fix browser-managed module preloads.

The image keeps the prefix at the points where URLs are generated:

- The [shared HTTP response adapter](compat/dashboard_prefix.py) uses upstream
  prefix validation and inserts one `<base href="/ui/<instance>/">` in every
  HTML document. Root deployments get `<base href="/">`. It also prefixes
  origin-relative document assets, CSS references, and HTTP redirects once.
- The Dashboard builds with Vite's [`--base=./` option](https://vite.dev/guide/build#relative-base).
  Initial assets resolve against the document base; lazy imports and their CSS
  resolve relative to the importing module. The same build works at root,
  under an instance prefix, and on a directly opened nested route.
- The login template resolves its submission URL against `document.baseURI`.
  The shared auth completion path adds the validated prefix to local return
  paths. The SPA retains upstream `__HERMES_BASE_PATH__` handling for API and
  WebSocket URLs.

There are no global fetch, XHR, WebSocket, or EventSource overrides. Cookies
and authorization remain owned by Hermes; ingress ownership checks remain
unchanged. An origin-root request has already lost its instance identity,
so ingress cannot repair it without guessing or introducing shared routes.

The adapter processes UTF-8 HTML/CSS documents up to 8 MiB. Attachments,
already encoded bodies, non-UTF-8 content, JSON, SSE, and WebSocket messages
pass through without content rewriting. It does not interpret arbitrary
application JSON strings as URLs or rewrite JavaScript source text.

There is no HTTP middleware registration API in this pinned release.
[The build-time installer](compat/apply.py) therefore changes three integration
points: the server wrapper, login submission, and shared auth return path.
It checks all three original file hashes before changing any file. An upstream
source change fails the image build and requires review.
Remove this adapter when an upstream release passes the root and prefixed
browser regressions without it. Do not replace it with per-endpoint ingress
exceptions or routing guesses based on Referer or cookies.

## Upgrade procedure

1. Confirm the current stable release on the
   [Hermes releases page](https://github.com/NousResearch/hermes-agent/releases).
2. Resolve the tag to its commit and download the matching source archive.
3. Update `HERMES_VERSION`, `HERMES_RELEASE`, `HERMES_GIT_SHA`, and
   `HERMES_SOURCE_SHA256` together in `Dockerfile`.
4. Update `profile.yaml` to the same package version.
5. Compare the new upstream `pyproject.toml`, `uv.lock`, `package.json`,
   `scripts/install.sh`, Dockerfile, gateway/dashboard commands, plugin API, and
   configuration keys with the current CetiRun integration. Recheck the pinned
   uv, Node, and SQLite images/sources and their checksums.
6. Remove obsolete compatibility code only when the new public interface
   replaces it. Do not retain imports from private `hermes_cli` functions.
7. Regenerate the Hermes native render fixtures and run the checks below.

## Required checks

```bash
python3 -m unittest \
  scripts.tests.test_cetirun_image_dependencies \
  scripts.tests.test_harness_assets -v
make python-test
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
go -C app/cetirun-operator test ./internal/agents/... ./internal/helm/... ./internal/controller/... -count=1
npm --prefix app/cetirun-portal/web test
npm --prefix app/cetirun-portal/web run build
```

Build the shared base and Hermes image for `linux/amd64`. Qualify the runtime
in an isolated container with synthetic configuration and no external network.
The runtime checks should cover:

- the installed package version and prebuilt Dashboard/TUI assets;
- SQLite WAL persistence and FTS5 trigram search;
- actual Teams plugin discovery, connection, reconnect, and mocked delivery;
- Gateway health and the web-terminal endpoint;
- Dashboard rejection of unauthenticated requests and incorrect passwords,
  followed by password login and a session-authenticated request;
- the CetiRun auth helper with and without its bearer key.

These checks use synthetic files and credentials. Model responses, real
Copilot login, Teams delivery, Chrome/CDP, Tailscale, and Kubernetes ingress
still require separately authorized integrations; these checks do not claim
to validate them.

```bash
docker buildx build --platform linux/amd64 \
  -f app/cetirun-chart/Dockerfile.base \
  -t cetirun-base:validation --load app/cetirun-chart
docker buildx build --platform linux/amd64 \
  --build-arg BASE_IMAGE=cetirun-base:validation \
  -f app/cetirun-chart/agents/hermes/Dockerfile \
  -t hermes-gateway:validation --load app/cetirun-chart
```

For rendered verification, use a synthetic local Dashboard behind an
ingress-like loopback proxy and check:

- desktop and mobile login/logout, redirects, asset paths, and cookie isolation
  between two prefixes;
- Chat opened from navigation, by direct URL, and after a full-page refresh;
- terminal output and draft input echoed through the authenticated PTY
  WebSocket, without submitting a prompt to a model;
- a nested profile route, plus the same flows at root.

Include a failing-image control when reproducing the login or lazy CSS preload
regression. Keep screenshots and results outside the repository, and use a
fresh browser profile without real credentials.

The synthetic fixture has no Copilot credentials. Its side panel may show that
the model is not configured. Record that expected warning separately from
unexpected console warnings or errors. Terminal rendering and draft
echo do not establish that a model can respond.
