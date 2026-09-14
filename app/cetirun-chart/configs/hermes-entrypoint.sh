#!/bin/bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -e

export HERMES_HOME="${HERMES_HOME:-/home/vibe/.hermes}"
PYTHON_BIN="${HERMES_PYTHON:-/usr/local/lib/hermes-agent/bin/python}"
if [ ! -x "$PYTHON_BIN" ]; then
  PYTHON_BIN="python3"
fi

if [ ! -f "$HERMES_HOME/config.yaml" ]; then
  echo "ERROR: config.yaml missing from $HERMES_HOME" >&2
  exit 1
fi
if [ ! -f "$HERMES_HOME/.env" ]; then
  echo "ERROR: .env missing from $HERMES_HOME" >&2
  exit 1
fi
if ! "$PYTHON_BIN" - "$HERMES_HOME/config.yaml" <<'PY' >/dev/null 2>&1
import sys
import yaml
with open(sys.argv[1], "r", encoding="utf-8") as f:
    yaml.safe_load(f)
PY
then
  echo "ERROR: config.yaml has invalid YAML syntax" >&2
  exit 1
fi

# Hermes reads ~/.hermes/.env directly — no need to export into process env.
# Only export API_SERVER_* vars needed by supervisord %(ENV_...)s substitution.
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    '#'*|'') continue ;;
    API_SERVER_*) ;;
    *) continue ;;
  esac
  key="${line%%=*}"
  value="${line#*=}"
  export "$key=$value"
done < "$HERMES_HOME/.env"

# CETIRUN_AUTH_KEY is the bearer key for the in-pod cetirun-auth helper. The
# chart injects it as a container env var; if it's missing (e.g. an old chart
# rolling forward to a newer image), fall back to API_SERVER_KEY so the
# helper service still has a key to compare against.
export CETIRUN_AUTH_KEY="${CETIRUN_AUTH_KEY:-${API_SERVER_KEY:-}}"
export CETIRUN_AGENT_TYPE="${CETIRUN_AGENT_TYPE:-hermes}"

cat > /tmp/supervisord.conf << EOF
[unix_http_server]
file=/tmp/supervisor.sock

[supervisorctl]
serverurl=unix:///tmp/supervisor.sock

[rpcinterface:supervisor]
supervisor.rpcinterface_factory=supervisor.rpcinterface:make_main_rpcinterface

[supervisord]
nodaemon=true
logfile=/dev/null
logfile_maxbytes=0
pidfile=/tmp/supervisord.pid

[program:cetirun-auth]
command=/usr/local/lib/hermes-agent/bin/python /usr/local/share/cetirun/cetirun-auth-service.py
priority=20
autorestart=true
startretries=5
environment=HERMES_HOME="%(ENV_HERMES_HOME)s",CETIRUN_AGENT_TYPE="hermes",CETIRUN_AUTH_KEY="%(ENV_CETIRUN_AUTH_KEY)s",API_SERVER_KEY="%(ENV_API_SERVER_KEY)s"
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0

[program:hermes-gateway]
command=/bin/bash -lc 'mkdir -p "$HERMES_HOME/logs"; exec hermes gateway run --replace --accept-hooks 2>&1 | tee -a "$HERMES_HOME/logs/gateway.log"'
priority=40
autorestart=true
stopasgroup=true
killasgroup=true
environment=HERMES_HOME="%(ENV_HERMES_HOME)s"
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0

EOF

if [ "${HERMES_DASHBOARD_ENABLED:-true}" = "true" ]; then
  if ! echo "${HERMES_DASHBOARD_PORT}" | grep -qE '^[0-9]+$' || [ "${HERMES_DASHBOARD_PORT}" -lt 1024 ] || [ "${HERMES_DASHBOARD_PORT}" -gt 65535 ]; then
    echo "ERROR: HERMES_DASHBOARD_PORT must be a number between 1024 and 65535, got '${HERMES_DASHBOARD_PORT}'" >&2
    exit 1
  fi
  # The dashboard is protected by the existing oauth2-proxy and NetworkPolicy.
  # Never expose port 9119 directly outside the cluster.
  cat >> /tmp/supervisord.conf << 'DASHBOARD_EOF'

[program:hermes-dashboard]
command=hermes dashboard --host 0.0.0.0 --port %(ENV_HERMES_DASHBOARD_PORT)s --no-open --skip-build --insecure
priority=30
autorestart=true
startretries=5
environment=HERMES_HOME="%(ENV_HERMES_HOME)s"
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
DASHBOARD_EOF
fi

if [ "${TTYD_ENABLED}" = "true" ]; then
  if ! echo "${TTYD_PORT}" | grep -qE '^[0-9]+$' || [ "${TTYD_PORT}" -lt 1024 ] || [ "${TTYD_PORT}" -gt 65535 ]; then
    echo "ERROR: TTYD_PORT must be a number between 1024 and 65535, got '${TTYD_PORT}'" >&2
    exit 1
  fi
  if [ ! -x "${TTYD_SHELL}" ]; then
    echo "ERROR: TTYD_SHELL must be an executable path, got '${TTYD_SHELL}'" >&2
    exit 1
  fi
  if ! echo "${TTYD_BASE_PATH}" | grep -qE '^/[a-z0-9/-]+$'; then
    echo "ERROR: TTYD_BASE_PATH must match ^/[a-z0-9/-]+$, got '${TTYD_BASE_PATH}'" >&2
    exit 1
  fi
  cat >> /tmp/supervisord.conf << 'TTYD_EOF'

[program:ttyd]
command=ttyd --port %(ENV_TTYD_PORT)s --base-path %(ENV_TTYD_BASE_PATH)s --writable --check-origin %(ENV_TTYD_SHELL)s
priority=25
autorestart=true
startretries=5
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
TTYD_EOF
fi

if [ "$TAILSCALE_ENABLED" = "true" ]; then
  if [ -n "$TAILSCALE_HOSTNAME" ]; then
    POD_ORDINAL=${HOSTNAME##*-}
    if echo "$POD_ORDINAL" | grep -qE '^[0-9]+$'; then
      TS_HOST="${TAILSCALE_HOSTNAME}-${POD_ORDINAL}"
    else
      TS_HOST="${TAILSCALE_HOSTNAME}-${HOSTNAME}"
    fi
  else
    TS_HOST="$HOSTNAME"
  fi
  TS_HOST=$(printf '%s' "$TS_HOST" | tr -cd 'a-zA-Z0-9-' | tr '[:upper:]' '[:lower:]' | head -c 63)
  if [ -z "$TS_HOST" ]; then
    echo "ERROR: Tailscale hostname resolved to empty after sanitization" >&2
    exit 1
  fi
  export TS_HOST

  sudo mkdir -p /dev/net
  sudo mknod /dev/net/tun c 10 200 2>/dev/null || true
  sudo chmod 600 /dev/net/tun
  if [ ! -c /dev/net/tun ]; then
    echo "ERROR: Failed to create /dev/net/tun" >&2
    exit 1
  fi

  cat > /tmp/tailscale-up.sh << 'TSUP_SCRIPT'
#!/bin/bash
sleep 5
sudo tailscale up --authkey="$TS_AUTHKEY" --hostname="$TS_HOST" --accept-dns=false
TSUP_SCRIPT
  chmod +x /tmp/tailscale-up.sh

  cat >> /tmp/supervisord.conf << 'TAILSCALE'

[program:tailscaled]
command=sudo tailscaled --state=/var/lib/tailscale/tailscaled.state
priority=3
autorestart=true
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0

[program:tailscale-up]
command=/tmp/tailscale-up.sh
priority=4
startsecs=0
autorestart=unexpected
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
TAILSCALE

  if [ "$TAILSCALE_SERVE_ENABLED" = "true" ]; then
    cat > /tmp/tailscale-serve.sh << 'TSSERVE_SCRIPT'
#!/bin/bash
sleep 10
sudo tailscale serve --bg "http://localhost:${TAILSCALE_SERVE_PORT}"
TSSERVE_SCRIPT
    chmod +x /tmp/tailscale-serve.sh
    cat >> /tmp/supervisord.conf << 'TSSERVE'

[program:tailscale-serve]
command=/tmp/tailscale-serve.sh
priority=5
startsecs=0
autorestart=unexpected
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stderr
stderr_logfile_maxbytes=0
TSSERVE
  fi
fi

exec supervisord -n -c /tmp/supervisord.conf
