# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Check that the image's Hermes ends a turn at the CetiRun AI Gateway's answer
for refused provider credentials.

The plugin cetirun-gateway-errors classifies the answer, a 502 with the error
code cetirun_provider_credentials or cetirun_copilot_credentials, with Hermes's
hook transform_api_error_classification. This loads the plugin as Hermes does,
from a HOME that enables it, and classifies synthetic answers of the Gateway
with Hermes's own classifier: an answer with one of the codes must be an error
that a retry does not fix, and must keep the Gateway's message. Another 502
must keep Hermes's verdict.

Run it with hermes-python, with HERMES_HOME set to a new empty directory.
"""

import os
import sys
from pathlib import Path

CODES = ("cetirun_provider_credentials", "cetirun_copilot_credentials")
MESSAGE = "CetiRun AI Gateway: the provider of the model synthetic-model refused the credentials of the AI Gateway."


def fail(reason):
    print(f"cetirun-gateway-errors: {reason}", file=sys.stderr)
    raise SystemExit(1)


def answer(code):
    """The OpenAI SDK's error for a 502 of the Gateway with the code."""
    import httpx
    import openai

    error = {"message": MESSAGE, "type": "internal_server_error", "param": None, "code": code}
    request = httpx.Request("POST", "http://gateway.invalid/v1/chat/completions")
    response = httpx.Response(502, json={"error": error}, request=request)
    return openai.InternalServerError(f"Error code: 502 - {error}", response=response, body=error)


def main():
    home = Path(os.environ.get("HERMES_HOME", ""))
    if not home.is_absolute() or not home.is_dir() or any(home.iterdir()):
        fail("HERMES_HOME must be a new empty directory")
    (home / "config.yaml").write_text("plugins:\n  enabled:\n    - cetirun-gateway-errors\n", encoding="utf-8")

    from agent.error_classifier import classify_api_error
    from hermes_cli.plugins import discover_plugins

    discover_plugins()
    for code in CODES:
        verdict = classify_api_error(answer(code), provider="cetirun-gateway", model="synthetic-model")
        if verdict.retryable or verdict.should_compress or verdict.reason.value != "unknown":
            fail(f"Hermes would retry the AI Gateway's answer with the code {code}: {verdict}")
        if not verdict.message.startswith("CetiRun AI Gateway:"):
            fail(f"Hermes does not keep the AI Gateway's message for the code {code}: {verdict.message!r}")
    other = classify_api_error(answer("502"), provider="cetirun-gateway", model="synthetic-model")
    if not other.retryable or other.reason.value == "unknown":
        fail(f"the plugin changed Hermes's verdict for another 502: {other}")
    print("Hermes ends a turn at the AI Gateway's answer for refused provider credentials")


if __name__ == "__main__":
    main()
