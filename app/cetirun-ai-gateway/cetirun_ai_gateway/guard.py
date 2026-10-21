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

A key the CetiRun operator issued reaches only the deployments in its access
groups, whichever way a request gets to them. LiteLLM checks a key against the
model the request names; when that is an auto router, the model the router
picks is not checked against the key, as LiteLLM's own access-group filter
stops when the key holds none of that model's groups. The operator grants a
router only to Agents that may use every model it sends requests to; this holds
each request to that, also before the operator has withdrawn a grant after a
configuration change. A model the key may not use at all is refused with 403,
which LiteLLM passes on unchanged, without retrying, falling back or cooling
the model down. When the key may use the model but none of its deployments is
available, LiteLLM's own error applies. The call a router makes to classify a
request is exempt: the router makes it on the key's behalf, to a model of its
own configuration that the key need not hold.

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
keeps it when it strips a marked item.
"""

import re
import sys

import litellm
from fastapi import HTTPException
from litellm._logging import verbose_proxy_logger
from litellm.integrations.custom_logger import CustomLogger
from litellm.proxy._types import UserAPIKeyAuth

# A request body field that makes LiteLLM log that request's content.
DEBUG_PARAMETER = "litellm_request_debug"
# Request fields that change the Gateway's retries for that request.
RETRY_PARAMETERS = ("num_retries", "model_group_retry_policy")
REQUEST_PARAMETERS = (DEBUG_PARAMETER, *RETRY_PARAMETERS)

# The access groups the CetiRun operator gives each Agent's GitHub Copilot
# routes: this prefix and the Agent's UID.
COPILOT_GROUP_PREFIX = "cetirun-copilot-"
# A key the CetiRun operator issued holds access groups under this prefix only
# (shared models, model routers, the Agent's Copilot routes), or, with none,
# LiteLLM's no-access placeholder. The Gateway chart refuses model names under
# the prefix, so a key granted models by name never looks like one.
OPERATOR_GROUP_PREFIX = "cetirun-"
NO_MODELS = "no-default-models"
# Where the proxy puts the calling key on a request: chat completions use
# metadata, the Responses and Anthropic Messages APIs litellm_metadata.
METADATA_BUCKETS = ("litellm_metadata", "metadata")
CALLER_KEY = "user_api_key_auth"
# How LiteLLM marks the call an auto router makes to classify a request. The
# proxy removes the field from the metadata a client sends.
CALL_ORIGIN = "internal_call_origin"
CLASSIFIER_ORIGIN = "autorouter_classifier"
# The proxy's module, which holds the live router. Read from sys.modules only:
# the callback never imports the proxy itself.
PROXY_SERVER_MODULE = "litellm.proxy.proxy_server"

# LiteLLM's message when no deployment of a model can take a request, and the
# list it appends: cooldown_list=['<deployment id>', ...].
NO_DEPLOYMENTS = "No deployments available"
COOLDOWN_LIST = re.compile(r",?\s*cooldown_list=\[[^\]]*\]")

# The marks LiteLLM's encrypted content affinity puts on the items it returns:
# the producing deployment in the item's ID, and in its encrypted_content.
MARKED_ITEM_ID = "encitem_"
MARKED_CONTENT = "litellm_enc:"
# LiteLLM's callback for encrypted content affinity, found by name so the
# check needs no import of LiteLLM's internals.
AFFINITY_CHECK = "EncryptedContentAffinityCheck"


def unmarked_encrypted_reasoning(item):
    """Whether an input item is encrypted reasoning that carries no mark of the deployment that produced it."""
    if not isinstance(item, dict) or item.get("type") != "reasoning":
        return False
    content = item.get("encrypted_content")
    if not isinstance(content, str) or not content or content.startswith(MARKED_CONTENT):
        return False
    item_id = item.get("id")
    return not (isinstance(item_id, str) and item_id.startswith(MARKED_ITEM_ID))


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
    """Remove the encrypted content of unmarked reasoning items, in place; the number of items changed.

    Like LiteLLM's own stripping, an item keeps its readable summary without
    encrypted_content and id, and an item with nothing readable is dropped.
    The list is changed in place: LiteLLM forwards this list, and its fallback
    hops share it.
    """
    if not isinstance(request_input, list):
        return 0
    kept, changed = [], 0
    for item in request_input:
        if not unmarked_encrypted_reasoning(item):
            kept.append(item)
            continue
        changed += 1
        if readable(item.get("summary")) or readable(item.get("content")):
            kept.append({key: value for key, value in item.items() if key not in ("encrypted_content", "id")})
    if changed:
        request_input[:] = kept
    return changed


def access_groups(deployment):
    """The access groups a deployment belongs to."""
    info = deployment.get("model_info") if isinstance(deployment, dict) else None
    groups = info.get("access_groups") if isinstance(info, dict) else None
    if not isinstance(groups, (list, tuple)):
        return ()
    return tuple(group for group in groups if isinstance(group, str))


def copilot_groups(deployment):
    """The per-Agent GitHub Copilot access groups a deployment belongs to."""
    return tuple(group for group in access_groups(deployment) if group.startswith(COPILOT_GROUP_PREFIX))


def caller_key(request_kwargs):
    """The calling key the proxy put on the request, or None when there is none."""
    if not isinstance(request_kwargs, dict):
        return None
    for name in METADATA_BUCKETS:
        bucket = request_kwargs.get(name)
        if isinstance(bucket, dict) and bucket.get(CALLER_KEY) is not None:
            return bucket[CALLER_KEY]
    return None


def caller_models(request_kwargs):
    """The models the calling key was granted, or None when the caller is unknown.

    An empty list, as the master key has, means every model to LiteLLM; it
    holds no access group, so it reaches no Copilot route.
    """
    caller = caller_key(request_kwargs)
    if caller is None:
        return None
    models = caller.get("models") if isinstance(caller, dict) else getattr(caller, "models", None)
    return [model for model in models if isinstance(model, str)] if isinstance(models, (list, tuple)) else []


def operator_groups(caller):
    """The access groups of a key the CetiRun operator issued, or None for any other caller.

    Only the key the proxy authenticated counts, as LiteLLM's UserAPIKeyAuth,
    never a dict. The master key, which has no models, keys granted "*",
    every model or models by name, are other callers.
    """
    if not isinstance(caller, UserAPIKeyAuth):
        return None
    models = caller.models
    if not models or not isinstance(models, (list, tuple)):
        return None
    if not all(isinstance(name, str) and (name.startswith(OPERATOR_GROUP_PREFIX) or name == NO_MODELS)
               for name in models):
        return None
    return frozenset(models)


def classifier_call(request_kwargs):
    """Whether the request is the call an auto router makes to classify a request."""
    return isinstance(request_kwargs, dict) and any(
        isinstance(bucket := request_kwargs.get(name), dict) and bucket.get(CALL_ORIGIN) == CLASSIFIER_ORIGIN
        for name in METADATA_BUCKETS
    )


def model_groups(model, deployments, team_id):
    """The access groups of every deployment of the model, available or not.

    The callback is given only the deployments still available; the live
    router knows the rest. Without it, the deployments given are all there is
    to go on.
    """
    router = getattr(sys.modules.get(PROXY_SERVER_MODULE), "llm_router", None)
    if router is not None:
        try:
            return set(router.get_model_access_groups(model_name=model, team_id=team_id))
        except Exception:  # the answer only chooses between two refusals; fall back to the deployments given
            pass
    return {group for deployment in deployments for group in access_groups(deployment)}


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


class ModelRefused(HTTPException):
    """The refusal of a model the calling key may not use.

    LiteLLM treats a 403 HTTPException from a callback as a guardrail's
    decision: it does not retry it, fall back or cool the model down, and the
    caller gets a 403 permission_error with the detail as its message. Without
    deployments LiteLLM would answer "No deployments available" with 429, which
    agents retry; the message must not contain that text, which the proxy maps
    to 429 too.
    """

    def __init__(self, model):
        super().__init__(status_code=403, detail=f"This key may not use the model {model}.")
        self.refused_model = model


class Guard(CustomLogger):
    """Request sanitization, the deployments a key reaches, and caller-facing errors."""

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
        """Leave out the deployments the calling key may not use.

        For every caller, the Copilot routes whose access group the key does
        not hold. For a key the operator issued, then also every deployment
        outside its access groups, except on a router's classifier call. An
        auto router's tier selection goes through this again with the same
        request, so a tier is held to the same rules. While encrypted content
        affinity applies to the model, unmarked encrypted reasoning is removed
        from a Responses API request first.
        """
        if isinstance(request_kwargs, dict) and isinstance(request_kwargs.get("input"), list) and affinity_enabled(model):
            removed = strip_unmarked_reasoning(request_kwargs["input"])
            if removed:
                verbose_proxy_logger.warning(
                    "CetiRun guard: removed the encrypted content of %d reasoning item%s without the Gateway's mark "
                    "from a request for %s; the deployment it reaches may not be able to read it.",
                    removed, "" if removed == 1 else "s", model)
        granted = set(caller_models(request_kwargs) or ())
        kept = [
            deployment
            for deployment in healthy_deployments
            if not (groups := copilot_groups(deployment)) or granted.intersection(groups)
        ]
        caller = caller_key(request_kwargs)
        held = operator_groups(caller)
        if held is None or not kept or classifier_call(request_kwargs):
            return kept
        allowed = [deployment for deployment in kept if held.intersection(access_groups(deployment))]
        if allowed:
            return allowed
        # Nothing left, though the key may use the model: its deployments are
        # unavailable (cooldown, health checks), which LiteLLM reports itself.
        if held.intersection(model_groups(model, healthy_deployments, getattr(caller, "team_id", None))):
            return allowed
        raise ModelRefused(model)

    async def async_post_call_failure_hook(self, request_data, original_exception, user_api_key_dict,
                                           traceback_str=None):
        # Logged here rather than where it is raised: a router also asks the
        # filter whether its choice can be served, so a refused request passes
        # through the filter more than once, and through this hook once.
        if isinstance(original_exception, ModelRefused):
            verbose_proxy_logger.warning(
                "CetiRun guard: refused the model %s to the key %s, which holds none of the model's access groups",
                original_exception.refused_model, getattr(user_api_key_dict, "key_alias", None))
        remove_cooldown_list(original_exception)
        return None


handler = Guard()
