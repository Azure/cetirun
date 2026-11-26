# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""CetiRun AI Gateway errors for Hermes.

When a provider refuses the CetiRun AI Gateway's own credentials for a model,
the Gateway answers 502 with the OpenAI error code
cetirun_provider_credentials, or cetirun_copilot_credentials for the Agent's
GitHub Copilot route. Hermes 0.22.7 classifies a 502 as a server error and
retries the request: three attempts, then five more rounds over about four
minutes. Credentials do not come back by themselves, so the hook
transform_api_error_classification classifies these answers as errors that a
retry does not fix. Hermes ends the turn instead of retrying it, and shows its
own error with the Gateway's message in the details. Other errors keep
Hermes's classification.
"""

CREDENTIALS_CODES = frozenset({"cetirun_provider_credentials", "cetirun_copilot_credentials"})


def classify(error_code=None, **_):
    """transform_api_error_classification: Hermes reads the code from the body of the answer."""
    if isinstance(error_code, str) and error_code.strip() in CREDENTIALS_CODES:
        return {"reason": "unknown", "retryable": False}
    return None


def register(ctx):
    ctx.register_hook("transform_api_error_classification", classify)
