#!/app/.venv/bin/python
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Entrypoint of the CetiRun AI Gateway image.

``cetirun-ai-gateway migrate`` applies LiteLLM's database migrations. Any other
arguments start the LiteLLM proxy with them.

Starting the proxy never touches a database by itself. The same image serves
the shared Gateway, which has PostgreSQL, and the per-agent Copilot proxy, which
has none, so migrating is a separate, explicit step.
"""

import os
import pathlib
import signal
import subprocess
import sys
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

# Serialises migrations across pods. Distinct from the key prisma locks itself.
MIGRATION_LOCK_KEY = 0x4345544952554E  # "CETIRUN"

# Query parameters libpq understands with the same meaning prisma gives them.
# The rest, such as connection_limit or schema, are prisma's own.
LIBPQ_PARAMETERS = {"sslmode", "connect_timeout", "application_name"}


def migrations_schema():
    """Locate LiteLLM's schema by the migrations beside it.

    Several copies of schema.prisma exist in a LiteLLM installation and only
    some have a migrations directory next to them. Pointed at one without,
    ``prisma migrate deploy`` finds nothing to apply, exits zero, and leaves an
    empty database behind a successful install. So a schema is accepted only
    when migrations sit beside it.
    """
    import litellm_proxy_extras

    directory = pathlib.Path(litellm_proxy_extras.__file__).parent
    schema = directory / "schema.prisma"
    migrations = directory / "migrations"
    count = sum(1 for entry in migrations.iterdir() if entry.is_dir()) if migrations.is_dir() else 0
    if not schema.is_file() or count == 0:
        sys.exit(f"no schema.prisma with migrations beside it in {directory}")
    return schema, count


def libpq_url(url):
    """The DATABASE_URL, reduced to what libpq accepts."""
    parts = urlsplit(url)
    query = [(key, value) for key, value in parse_qsl(parts.query) if key in LIBPQ_PARAMETERS]
    return urlunsplit(parts._replace(query=urlencode(query)))


def connect(url, deadline):
    """Connect, waiting for a database that is still starting.

    The bundled PostgreSQL starts in the same release as the Gateway, so the
    first pods usually get here before it accepts connections.
    """
    import psycopg

    while True:
        try:
            return psycopg.connect(libpq_url(url), autocommit=True, connect_timeout=5)
        except psycopg.OperationalError as error:
            if time.monotonic() >= deadline:
                sys.exit(f"database not reachable: {error}")
            print(f"waiting for the database: {str(error).strip().splitlines()[0]}", flush=True)
            time.sleep(3)


def migrate():
    # This process is PID 1 of its container, and the kernel ignores signals a
    # PID 1 has no handler for. Without this, a pod stopped while waiting for
    # its database would sit out the whole termination grace period.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(128 + signal.SIGTERM))

    url = os.environ.get("DATABASE_URL")
    if not url:
        sys.exit("DATABASE_URL is not set")
    deadline = time.monotonic() + int(os.environ.get("MIGRATE_WAIT_SECONDS", "600"))
    connection = connect(url, deadline)

    # Only one pod migrates at a time. prisma's own advisory lock is not enough:
    # a second prisma waits for it inside an open transaction, and LiteLLM's
    # migrations include CREATE INDEX CONCURRENTLY, which waits for every open
    # transaction. The two wait on each other, and PostgreSQL resolves it by
    # killing one - possibly the one migrating, leaving a failed migration that
    # blocks every later deploy. Polling with pg_try_advisory_lock keeps each
    # attempt a short transaction, and the holder's connection stays idle, so
    # neither blocks the index build.
    while not connection.execute("SELECT pg_try_advisory_lock(%s)", (MIGRATION_LOCK_KEY,)).fetchone()[0]:
        if time.monotonic() >= deadline:
            sys.exit("timed out waiting for another pod's migration to finish")
        print("another pod is migrating; waiting", flush=True)
        time.sleep(2)

    schema, count = migrations_schema()
    print(f"applying migrations from {schema} ({count} migrations)", flush=True)
    result = subprocess.run(["prisma", "migrate", "deploy", "--schema", str(schema)], check=False)
    connection.close()
    sys.exit(result.returncode)


def main():
    args = sys.argv[1:]
    if args[:1] == ["migrate"]:
        migrate()
    os.execvp("litellm", ["litellm", *args])


if __name__ == "__main__":
    main()
