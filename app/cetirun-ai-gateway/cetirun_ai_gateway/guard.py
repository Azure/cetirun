# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Checks the CetiRun AI Gateway applies to every request.

The image's entrypoint registers ``handler`` with LiteLLM before the proxy
loads its configuration, always, unlike the request log, which the chart can
turn off. Settings saved in LiteLLM's database cannot remove it either.

Request fields a caller could use to change the Gateway for its own call are
dropped:

- litellm_request_debug makes LiteLLM print that request's prompt and the
  provider's raw response to the log.
- num_retries, in the body or the x-litellm-num-retries header, which LiteLLM
  copies into num_retries before this callback sees the request, and
  model_group_retry_policy turn the Gateway's retries back on. The Gateway does
  not retry: agents retry on their own, and the two multiply. Each made one
  failing request four upstream calls.

A per-Agent GitHub Copilot route, a deployment in an access group named
cetirun-copilot-<Agent UID>, is offered only to a key that holds that group.
LiteLLM lets a key granted every model, such as the master key, reach every
deployment; a key reaches these only if it holds the route's access group.

Error messages returned to callers no longer include LiteLLM's internal list of
deployments in cooldown: when no deployment of a model is available, LiteLLM's
message lists the IDs of every deployment in cooldown, of any model. The proxy
builds the response from the error after the failure hook runs, so the hook
removes the list from the error's message in place. The status (429), the
"Try again in N seconds" wording and the retry-after header, which LiteLLM
derives from the error, stay as they are.

Encrypted reasoning without the Gateway's mark is removed from Responses API
requests while encrypted content affinity is on for the model. A client that
keeps no state on the provider, such as OpenClaw, sends every earlier
reasoning item back, encrypted by the deployment that produced it, which alone
can read it. With affinity on, LiteLLM marks each item it returns with that
deployment and routes or strips by the mark; an item without one, from before
affinity was turned on or from elsewhere, is forwarded as it is, and a
deployment of another provider account refuses the whole request (400
invalid_encrypted_content). The item keeps its readable summary, as LiteLLM
keeps it when it strips a marked item. Encrypted content a client shortened,
which no deployment can read, is removed the same way.

The mark is written in a form clients keep. LiteLLM writes it as
litellm_enc:<base64>;<content>; OpenClaw keeps encrypted reasoning only when
it looks like an opaque token (letters, digits, + / _ - and trailing =), and
otherwise stores it through its secret redaction, which shortens the
provider's gAAAA... content to 11 characters. Every following turn then sent
that back, the deployment refused it, and OpenClaw retried without its
reasoning. The guard replaces LiteLLM's two functions that write and read the
mark: it writes ctrnenc<hex of the deployment ID>x<content>, and reads both
forms, so items marked before the change still route.

A request whose encrypted reasoning a deployment could not read (400
invalid_encrypted_content) does not go on to the model's fallbacks: their
deployments cannot read it either, so each fallback fails the same way, and
the client, which handles this error by sending the request again without the
reasoning, gets the first error instead of the last.
"""

import re

import litellm
from litellm._logging import verbose_proxy_logger
from litellm.integrations.custom_logger import CustomLogger

# A request body field that makes LiteLLM log that request's content.
DEBUG_PARAMETER = "litellm_request_debug"
# Request fields that change the Gateway's retries for that request.
RETRY_PARAMETERS = ("num_retries", "model_group_retry_policy")
REQUEST_PARAMETERS = (DEBUG_PARAMETER, *RETRY_PARAMETERS)

# The access groups the CetiRun operator gives each Agent's GitHub Copilot
# routes: this prefix and the Agent's UID.
COPILOT_GROUP_PREFIX = "cetirun-copilot-"
# Where the proxy puts the calling key on a request: chat completions use
# metadata, the Responses and Anthropic Messages APIs litellm_metadata.
METADATA_BUCKETS = ("litellm_metadata", "metadata")
CALLER_KEY = "user_api_key_auth"

# LiteLLM's message when no deployment of a model can take a request, and the
# list it appends: cooldown_list=['<deployment id>', ...].
NO_DEPLOYMENTS = "No deployments available"
COOLDOWN_LIST = re.compile(r",?\s*cooldown_list=\[[^\]]*\]")

# The marks LiteLLM's encrypted content affinity puts on the items it returns:
# the producing deployment in the item's ID, and in its encrypted_content.
MARKED_ITEM_ID = "encitem_"
MARKED_CONTENT = "litellm_enc:"
# The Gateway's form of the mark in encrypted_content: this prefix, the
# deployment ID in lowercase hex, an x, then the provider's content. Only
# characters an opaque token may hold.
GATEWAY_MARK = "ctrnenc"
GATEWAY_MARK_FORM = re.compile(r"ctrnenc((?:[0-9a-f]{2})+)x(.+)", re.DOTALL)
# A client's shortened copy of a long value: kept start, ellipsis, kept end.
SHORTENED = "\u2026"
# LiteLLM's callback for encrypted content affinity, found by name so the
# check needs no import of LiteLLM's internals.
AFFINITY_CHECK = "EncryptedContentAffinityCheck"
# How a provider refuses encrypted reasoning it cannot read.
UNREADABLE_REASONING = ("invalid_encrypted_content", "could not be verified", "could not be decrypted")


def mark_encrypted_content(encrypted_content, model_id):
    """encrypted_content with the producing deployment's ID, in the Gateway's form."""
    return f"{GATEWAY_MARK}{str(model_id).encode('utf-8').hex()}x{encrypted_content}"


def read_marked_content(content, legacy=None):
    """(deployment ID or None, provider content) of encrypted_content in either form of the mark.

    legacy reads LiteLLM's own litellm_enc: form; without it that form is
    returned unread.
    """
    if isinstance(content, str) and content.startswith(GATEWAY_MARK):
        match = GATEWAY_MARK_FORM.fullmatch(content)
        if match:
            try:
                return bytes.fromhex(match.group(1)).decode("utf-8"), match.group(2)
            except ValueError:
                pass
        return None, content
    if legacy is not None:
        return legacy(content)
    return None, content


def install_mark_form():
    """Make LiteLLM write the Gateway's form of the mark and read both forms."""
    try:
        from litellm.responses.utils import ResponsesAPIRequestUtils
    except Exception:  # noqa: BLE001
        return False
    current = ResponsesAPIRequestUtils.__dict__.get("_unwrap_encrypted_content_with_model_id")
    legacy = getattr(current, "__func__", current)
    if getattr(legacy, "cetirun_mark_form", False):
        return True

    def unwrap(wrapped_content):
        return read_marked_content(wrapped_content, legacy)

    unwrap.cetirun_mark_form = True
    ResponsesAPIRequestUtils._wrap_encrypted_content_with_model_id = staticmethod(mark_encrypted_content)
    ResponsesAPIRequestUtils._unwrap_encrypted_content_with_model_id = staticmethod(unwrap)
    return True


def marked_content(content):
    """Whether encrypted_content carries the producing deployment in either form of the mark."""
    if content.startswith(MARKED_CONTENT):
        return True
    return read_marked_content(content)[0] is not None


def unmarked_encrypted_reasoning(item):
    """Whether an input item is encrypted reasoning that carries no mark of the deployment that produced it."""
    if not isinstance(item, dict) or item.get("type") != "reasoning":
        return False
    content = item.get("encrypted_content")
    if not isinstance(content, str) or not content or marked_content(content):
        return False
    item_id = item.get("id")
    return not (isinstance(item_id, str) and item_id.startswith(MARKED_ITEM_ID))


def shortened_encrypted_reasoning(item):
    """Whether an input item is encrypted reasoning a client shortened, which no deployment can read."""
    if not isinstance(item, dict) or item.get("type") != "reasoning":
        return False
    content = item.get("encrypted_content")
    return isinstance(content, str) and SHORTENED in content


def readable(value):
    """Whether a reasoning item's summary or content carries text a model can read."""
    if isinstance(value, str):
        return bool(value.strip())
    if isinstance(value, list):
        return any(isinstance(block, dict) and bool(block.get("text")) for block in value)
    return False


def affinity_enabled(model):
    """Whether LiteLLM's encrypted content affinity applies to requests for the model."""
    for callback in getattr(litellm, "callbacks", None) or ():
        if type(callback).__name__ != AFFINITY_CHECK:
            continue
        enabled = getattr(callback, "_is_enabled_for_model_group", None)
        try:
            return bool(enabled(model)) if callable(enabled) else True
        except Exception:  # noqa: BLE001
            return True
    return False


def strip_unmarked_reasoning(request_input):
    """Remove unmarked or shortened encrypted reasoning, in place; the number of items changed.

    Like LiteLLM's own stripping, an item keeps its readable summary without
    encrypted_content and id, and an item with nothing readable is dropped.
    The list is changed in place: LiteLLM forwards this list, and its fallback
    hops share it.
    """
    if not isinstance(request_input, list):
        return 0
    kept, changed = [], 0
    for item in request_input:
        if not (unmarked_encrypted_reasoning(item) or shortened_encrypted_reasoning(item)):
            kept.append(item)
            continue
        changed += 1
        if readable(item.get("summary")) or readable(item.get("content")):
            kept.append({key: value for key, value in item.items() if key not in ("encrypted_content", "id")})
    if changed:
        request_input[:] = kept
    return changed


class UnreadableReasoningFallback(Exception):
    """Ends a fallback that follows a deployment's refusal of encrypted reasoning."""


def unreadable_reasoning_refusal(request_kwargs):
    """The refusal of a request's encrypted reasoning when the request is a fallback after it, else None."""
    if not isinstance(request_kwargs, dict):
        return None
    for name in METADATA_BUCKETS:
        bucket = request_kwargs.get(name)
        if not isinstance(bucket, dict) or not bucket.get("attempted_fallbacks"):
            continue
        for attempt in bucket.get("previous_models") or ():
            text = attempt.get("exception_string") if isinstance(attempt, dict) else None
            if isinstance(text, str) and any(marker in text for marker in UNREADABLE_REASONING):
                return text
    return None


def refusal_error(model, text):
    """The error that ends the fallback: the deployment's refusal again, as a 400 the client recognizes."""
    error_class = getattr(litellm, "BadRequestError", None)
    if error_class is not None:
        try:
            return error_class(message=text, model=model, llm_provider="")
        except Exception:  # noqa: BLE001
            pass
    return UnreadableReasoningFallback(text)


def copilot_groups(deployment):
    """The per-Agent GitHub Copilot access groups a deployment belongs to."""
    info = deployment.get("model_info") if isinstance(deployment, dict) else None
    groups = info.get("access_groups") if isinstance(info, dict) else None
    if not isinstance(groups, (list, tuple)):
        return ()
    return tuple(group for group in groups if isinstance(group, str) and group.startswith(COPILOT_GROUP_PREFIX))


def caller_models(request_kwargs):
    """The models the calling key was granted, or None when the caller is unknown.

    An empty list, as the master key has, means every model to LiteLLM; it
    holds no access group, so it reaches no Copilot route.
    """
    if not isinstance(request_kwargs, dict):
        return None
    for name in METADATA_BUCKETS:
        bucket = request_kwargs.get(name)
        if not isinstance(bucket, dict) or bucket.get(CALLER_KEY) is None:
            continue
        caller = bucket[CALLER_KEY]
        models = caller.get("models") if isinstance(caller, dict) else getattr(caller, "models", None)
        return [model for model in models if isinstance(model, str)] if isinstance(models, (list, tuple)) else []
    return None


def without_cooldown_list(text):
    """The text without LiteLLM's list of deployments in cooldown, or None when it has none."""
    if not isinstance(text, str) or NO_DEPLOYMENTS not in text or "cooldown_list=" not in text:
        return None
    return COOLDOWN_LIST.sub("", text)


def remove_cooldown_list(error):
    """Remove the list from the error's message in place. True when there was one."""
    changed = False
    arguments = getattr(error, "args", ())
    if isinstance(arguments, tuple):
        cleaned = tuple(without_cooldown_list(argument) or argument for argument in arguments)
        if cleaned != arguments:
            error.args = cleaned
            changed = True
    message = getattr(error, "message", None)
    cleaned = without_cooldown_list(message)
    if cleaned is not None:
        try:
            error.message = cleaned
            changed = True
        except (AttributeError, TypeError):
            pass
    return changed


class Guard(CustomLogger):
    """Request sanitization, per-Agent Copilot route separation and caller-facing errors."""

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        if isinstance(data, dict):
            for name in REQUEST_PARAMETERS:
                data.pop(name, None)
        return None

    async def async_filter_deployments(
        self,
        model,
        healthy_deployments,
        messages,
        request_kwargs=None,
        parent_otel_span=None,
    ):
        """Leave out the Copilot routes whose access group the calling key does not hold.

        Every other deployment is left as it is. An auto router's tier
        selection goes through this again with the same request, so a tier is
        held to the same rule. While encrypted content affinity applies to the
        model, unmarked or shortened encrypted reasoning is removed from a
        Responses API request first. A fallback after a deployment refused the
        request's encrypted reasoning is ended here, so the client gets that
        first error.
        """
        refusal = unreadable_reasoning_refusal(request_kwargs)
        if refusal is not None:
            verbose_proxy_logger.warning(
                "CetiRun guard: no fallback to %s: a deployment could not read the request's encrypted reasoning, "
                "and no other deployment can.", model)
            raise refusal_error(model, refusal)
        if isinstance(request_kwargs, dict) and isinstance(request_kwargs.get("input"), list) and affinity_enabled(model):
            removed = strip_unmarked_reasoning(request_kwargs["input"])
            if removed:
                verbose_proxy_logger.warning(
                    "CetiRun guard: removed the encrypted content of %d reasoning item%s without the Gateway's mark, "
                    "or shortened by the client, from a request for %s; no deployment may be able to read it.",
                    removed, "" if removed == 1 else "s", model)
        granted = set(caller_models(request_kwargs) or ())
        return [
            deployment
            for deployment in healthy_deployments
            if not (groups := copilot_groups(deployment)) or granted.intersection(groups)
        ]

    async def async_post_call_failure_hook(self, request_data, original_exception, user_api_key_dict,
                                           traceback_str=None):
        remove_cooldown_list(original_exception)
        return None


handler = Guard()
# The entrypoint imports this module before the proxy loads its configuration,
# so LiteLLM writes the Gateway's form of the mark from the first request.
install_mark_form()
