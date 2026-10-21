# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for the AI Gateway image's guard callback.

LiteLLM is not installed for these tests: the callback only subclasses
LiteLLM's CustomLogger, recognizes its UserAPIKeyAuth, logs to its proxy logger
and raises FastAPI's HTTPException, which are replaced by stand-ins. The
deployments, keys and errors below follow what LiteLLM 1.105.1 hands its
callbacks: deployments as model_list entries, the calling key as
UserAPIKeyAuth in the request's metadata, and RouterRateLimitError's message.
"""

import asyncio
import importlib.util
import logging
import pathlib
import sys
import types
import unittest

PACKAGE = pathlib.Path(__file__).resolve().parents[2] / "app/cetirun-ai-gateway/cetirun_ai_gateway"
# Stands in for LiteLLM's proxy logger.
LOGGER = "cetirun-ai-gateway-guard-test"
PROXY_SERVER = "litellm.proxy.proxy_server"


class CustomLogger:
    def __init__(self, *args, **kwargs):
        pass


class Key:
    """Stands in for LiteLLM's UserAPIKeyAuth, the key object the proxy authenticated."""

    def __init__(self, models, team_id=None, key_alias=None, token=None):
        self.models = models
        self.team_id = team_id
        self.key_alias = key_alias
        self.token = token


class HTTPException(Exception):
    """Shaped like FastAPI's: a status code and a detail."""

    def __init__(self, status_code, detail=None, headers=None):
        super().__init__(status_code, detail)
        self.status_code = status_code
        self.detail = detail
        self.headers = headers


def load_guard():
    stubs = {
        "fastapi": types.ModuleType("fastapi"),
        "litellm": types.ModuleType("litellm"),
        "litellm._logging": types.ModuleType("litellm._logging"),
        "litellm.integrations": types.ModuleType("litellm.integrations"),
        "litellm.integrations.custom_logger": types.ModuleType("litellm.integrations.custom_logger"),
        "litellm.proxy": types.ModuleType("litellm.proxy"),
        "litellm.proxy._types": types.ModuleType("litellm.proxy._types"),
    }
    stubs["fastapi"].HTTPException = HTTPException
    stubs["litellm._logging"].verbose_proxy_logger = logging.getLogger(LOGGER)
    stubs["litellm.integrations.custom_logger"].CustomLogger = CustomLogger
    stubs["litellm.proxy._types"].UserAPIKeyAuth = Key
    saved = {name: sys.modules.get(name) for name in stubs}
    sys.modules.update(stubs)
    try:
        spec = importlib.util.spec_from_file_location("gateway_guard", PACKAGE / "guard.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        for name, previous in saved.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


def live_router(groups, calls=None):
    """The proxy module with a live router that lists these access groups for any model."""

    class Router:
        def get_model_access_groups(self, model_name=None, model_access_group=None, team_id=None):
            if calls is not None:
                calls.append((model_name, team_id))
            return {group: [model_name] for group in groups}

    proxy = types.ModuleType(PROXY_SERVER)
    proxy.llm_router = Router()
    return proxy


def deployment(identifier, *groups):
    return {
        "model_name": "copilot/gpt-5" if any(g.startswith("cetirun-copilot-") for g in groups) else "gpt-5",
        "litellm_params": {"model": "openai/gpt-5", "api_base": "http://proxy.example.com"},
        "model_info": {"id": identifier, "access_groups": list(groups)},
    }


def chart_model(name, suffix):
    """A model from the Gateway chart's models with operatorIntegration: its own ID and access group."""
    return {
        "model_name": name,
        "litellm_params": {"model": f"openai/{name}", "api_base": "http://models.example.com"},
        "model_info": {"id": f"cetirun-shared-{suffix}", "access_groups": [f"cetirun-model-{suffix}"]},
    }


ALICE = deployment("cetirun-copilot-alice-gpt-5", "cetirun-copilot-alice")
BOB = deployment("cetirun-copilot-bob-gpt-5", "cetirun-copilot-bob")
SHARED = deployment("cetirun-shared-1234", "cetirun-shared", "cetirun-model-1234")
UNGROUPED = {"model_name": "gpt-5", "litellm_params": {"model": "openai/gpt-5"}, "model_info": {"id": "plain"}}
NO_INFO = {"model_name": "gpt-5", "litellm_params": {"model": "openai/gpt-5"}}
ALL = [ALICE, BOB, SHARED, UNGROUPED, NO_INFO]

# A router's tier models: small in the key's groups, large not. A deployment
# added in the console under the same name belongs to no group.
SMALL = chart_model("small", "aaaa")
LARGE = chart_model("large", "cccc")
LARGE_CONSOLE = {"model_name": "large", "litellm_params": {"model": "openai/large"}, "model_info": {"id": "console"}}
# The key of an Agent granted a router and its small tier, as the operator creates it.
ROUTER_KEY = Key(["cetirun-model-aaaa", "cetirun-router-0123456789abcdef"], team_id="cetirun-team-synthetic",
                 key_alias="cetirun-synthetic-agent-0000", token="synthetic-hashed-token")
REFUSAL = "This key may not use the model large."

COOLDOWN_MESSAGE = (
    "No deployments available for selected model, Try again in 5.0 seconds. All deployments for selected "
    "model are in cooldown. Passed model=copilot/gpt-5. pre-call-checks=False, "
    "cooldown_list=['cetirun-copilot-alice-gpt-5', 'cetirun-copilot-bob-gpt-5']"
)


class RouterRateLimitError(ValueError):
    """Shaped like LiteLLM's: the message is the only argument, cooldown_time an attribute."""

    def __init__(self, message, cooldown_time=5.0):
        self.cooldown_time = cooldown_time
        self.cooldown_list = ["cetirun-copilot-alice-gpt-5"]
        super().__init__(message)


class ProviderError(Exception):
    """Shaped like LiteLLM's provider exceptions: a message attribute next to the arguments."""

    def __init__(self, message):
        self.message = message
        self.status_code = 429
        super().__init__(message)


class EncryptedContentAffinityCheck:
    """Stands in for LiteLLM's callback of that name: only the per-group switch is read."""

    def __init__(self, enabled=True):
        self.enabled = enabled

    def _is_enabled_for_model_group(self, model_group):
        return self.enabled


# Encrypted reasoning as a stateless Responses API client sends it back: marked
# by LiteLLM in the encrypted content, in the ID, or not at all.
UNMARKED = {"type": "reasoning", "id": "rs_0123", "encrypted_content": "gAAAAunmarked",
            "summary": [{"type": "summary_text", "text": "Worked it out."}]}
UNMARKED_UNREADABLE = {"type": "reasoning", "id": "rs_4567", "encrypted_content": "gAAAAother", "summary": []}
MARKED_CONTENT = {"type": "reasoning", "encrypted_content": "litellm_enc:bW9kZWxfaWQ6YQ==;gAAAAmarked",
                  "summary": [{"type": "summary_text", "text": "Marked."}]}
MARKED_ID = {"type": "reasoning", "id": "encitem_bGl0ZWxsbTptb2RlbF9pZDphO2l0ZW1faWQ6cnNfMQ==",
             "encrypted_content": "gAAAAmarkedbyid", "summary": []}
CONVERSATION_TAIL = [
    {"type": "function_call", "call_id": "call_1", "name": "read", "arguments": "{}"},
    {"type": "function_call_output", "call_id": "call_1", "output": "ok"},
    {"role": "assistant", "content": "Done."},
]


class GuardTest(unittest.TestCase):
    def setUp(self):
        self.module = load_guard()
        self.guard = self.module.Guard()
        # No live router unless a test provides one.
        previous = sys.modules.pop(PROXY_SERVER, None)
        self.addCleanup(self.restore_proxy_server, previous)

    @staticmethod
    def restore_proxy_server(previous):
        sys.modules.pop(PROXY_SERVER, None)
        if previous is not None:
            sys.modules[PROXY_SERVER] = previous

    def filtered(self, request_kwargs, deployments=ALL, model="copilot/gpt-5"):
        return asyncio.run(self.guard.async_filter_deployments(
            model=model, healthy_deployments=list(deployments), messages=None,
            request_kwargs=request_kwargs))

    def refused(self, request_kwargs, deployments=(LARGE,), model="large"):
        """The error the filter raised, which must be the refusal."""
        with self.assertRaises(self.module.ModelRefused) as raised:
            self.filtered(request_kwargs, deployments, model)
        return raised.exception

    def ids(self, deployments):
        return [(d.get("model_info") or {}).get("id") for d in deployments]

    # ----- failover past key affinity -----

    def install_affinity(self):
        name = "litellm.router_utils.pre_call_checks.deployment_affinity_check"

        class DeploymentAffinityCheck:
            """Narrows to the deployment a key is kept on, like LiteLLM's callback."""

            pinned = "dep-a"

            async def async_filter_deployments(self, model, healthy_deployments, messages, request_kwargs=None,
                                               parent_otel_span=None):
                kept = [d for d in healthy_deployments if d["model_info"]["id"] == self.pinned]
                return kept or healthy_deployments

        module = types.ModuleType(name)
        module.DeploymentAffinityCheck = DeploymentAffinityCheck
        previous = sys.modules.get(name)
        sys.modules[name] = module
        self.addCleanup(lambda: sys.modules.__setitem__(name, previous) if previous else sys.modules.pop(name, None))
        self.assertTrue(self.module.install_failover_past_affinity())
        self.assertTrue(self.module.install_failover_past_affinity())  # once only
        return DeploymentAffinityCheck()

    def affinity_filtered(self, check, request_kwargs):
        deployments = [{"model_info": {"id": i}} for i in ("dep-a", "dep-b", "dep-c")]
        kept = asyncio.run(check.async_filter_deployments("m", deployments, None, request_kwargs=request_kwargs))
        return [d["model_info"]["id"] for d in kept]

    def test_a_failover_leaves_the_deployment_a_key_is_kept_on(self):
        check = self.install_affinity()
        self.assertEqual(self.affinity_filtered(check, {}), ["dep-a"])
        self.assertEqual(self.affinity_filtered(check, None), ["dep-a"])
        # The kept deployment failed this request: affinity chooses among the others.
        self.assertEqual(self.affinity_filtered(check, {"_excluded_deployment_ids": ["dep-a"]}), ["dep-b", "dep-c"])
        # Another deployment failed: the key stays where it is kept.
        self.assertEqual(self.affinity_filtered(check, {"_excluded_deployment_ids": ["dep-c"]}), ["dep-a"])

    def test_without_litellms_affinity_check_nothing_is_installed(self):
        self.assertFalse(self.module.install_failover_past_affinity())
    # ----- request fields -----

    def test_client_debug_output_is_always_dropped(self):
        data = {"model": "gpt-4o", "litellm_request_debug": True}
        result = asyncio.run(self.guard.async_pre_call_hook(None, None, data, "acompletion"))
        self.assertIsNone(result)
        self.assertEqual(data, {"model": "gpt-4o"})

    def test_callers_cannot_change_the_retries(self):
        # num_retries also carries the x-litellm-num-retries header: LiteLLM
        # copies it into the request before the callback runs. max_retries
        # stays: measured, it did not add an upstream call.
        data = {
            "model": "gpt-4o",
            "num_retries": 3,
            "model_group_retry_policy": {"gpt-4o": {"InternalServerErrorRetries": 3}},
            "max_retries": 3,
            "messages": [{"role": "user", "content": "hi"}],
        }
        asyncio.run(self.guard.async_pre_call_hook(None, None, data, "acompletion"))
        self.assertEqual(data, {"model": "gpt-4o", "max_retries": 3, "messages": [{"role": "user", "content": "hi"}]})

    def test_requests_that_are_not_bodies_are_left_alone(self):
        self.assertIsNone(asyncio.run(self.guard.async_pre_call_hook(None, None, None, "acompletion")))

    # ----- Copilot routes -----

    def test_an_agent_key_reaches_only_its_own_copilot_route(self):
        # The key the operator issued also reaches nothing outside its groups.
        for bucket in ("metadata", "litellm_metadata"):
            with self.subTest(bucket=bucket):
                kept = self.filtered({bucket: {"user_api_key_auth": Key(["cetirun-shared", "cetirun-copilot-alice"])}})
                self.assertEqual(self.ids(kept), ["cetirun-copilot-alice-gpt-5", "cetirun-shared-1234"])

    def test_keys_granted_every_model_reach_no_copilot_route(self):
        # The master key has no models list: LiteLLM reads an empty one as
        # every model. "*", all-proxy-models and model names grant no group.
        for models in ([], ["*"], ["all-proxy-models"], ["copilot/gpt-5", "gpt-5"], None):
            with self.subTest(models=models):
                kept = self.filtered({"metadata": {"user_api_key_auth": Key(models)}})
                self.assertEqual(self.ids(kept), ["cetirun-shared-1234", "plain", None])

    def test_a_group_name_must_match_exactly(self):
        for name in ("cetirun-copilot-", "cetirun-copilot-alic", "CETIRUN-COPILOT-ALICE", "cetirun-copilot-*"):
            with self.subTest(name=name):
                kept = self.ids(self.filtered({"metadata": {"user_api_key_auth": Key([name, "cetirun-shared"])}}))
                self.assertNotIn("cetirun-copilot-alice-gpt-5", kept)
                self.assertNotIn("cetirun-copilot-bob-gpt-5", kept)
                self.assertIn("cetirun-shared-1234", kept)

    def test_a_key_given_as_a_dict_is_read_too(self):
        # For the Copilot routes only: a dict is never taken for a key the
        # operator issued.
        kept = self.filtered({"litellm_metadata": {"user_api_key_auth": {"models": ["cetirun-copilot-bob"]}}})
        self.assertEqual(self.ids(kept), ["cetirun-copilot-bob-gpt-5", "cetirun-shared-1234", "plain", None])

    def test_an_unknown_caller_reaches_no_copilot_route(self):
        for request_kwargs in (None, {}, {"metadata": {}}, {"metadata": {"user_api_key_auth": None}}, {"metadata": "x"}):
            with self.subTest(request_kwargs=request_kwargs):
                self.assertEqual(self.ids(self.filtered(request_kwargs)), ["cetirun-shared-1234", "plain", None])

    def test_keys_granted_every_model_keep_every_other_deployment(self):
        kept = self.filtered({"metadata": {"user_api_key_auth": Key([])}}, [SHARED, UNGROUPED, NO_INFO])
        self.assertEqual(kept, [SHARED, UNGROUPED, NO_INFO])

    def test_an_auto_router_tier_is_held_to_the_same_rule(self):
        # LiteLLM routes a tier with the request that reached the router, so the
        # calling key is the same one.
        request = {"metadata": {"user_api_key_auth": Key(["auto-coding"])}}
        self.assertEqual(self.ids(self.filtered(request, [ALICE, SHARED])), ["cetirun-shared-1234"])

    def test_a_merged_name_gives_a_key_holding_only_its_copilot_group_its_own_route(self):
        # A shared deployment and the Agents' Copilot routes under one name.
        request = {"metadata": {"user_api_key_auth": Key(["cetirun-copilot-alice"])}}
        self.assertEqual(self.ids(self.filtered(request, [ALICE, BOB, SHARED])), ["cetirun-copilot-alice-gpt-5"])

    # ----- keys the operator issued, through routers -----

    def test_an_operator_key_reaches_only_the_deployments_in_its_groups(self):
        request = {"metadata": {"user_api_key_auth": Key(["cetirun-model-cccc"])}}
        self.assertEqual(self.filtered(request, [LARGE, LARGE_CONSOLE], "large"), [LARGE])

    def test_a_tier_the_key_may_not_use_is_refused_with_403(self):
        # LiteLLM checked the key against the router, then let the tier the
        # router picked through: the key holds none of its groups.
        for bucket in ("metadata", "litellm_metadata"):
            with self.subTest(bucket=bucket):
                error = self.refused({bucket: {"user_api_key_auth": ROUTER_KEY}}, [LARGE, LARGE_CONSOLE])
                self.assertIsInstance(error, HTTPException)
                self.assertEqual((error.status_code, error.detail), (403, REFUSAL))
                # The proxy answers 429 for these.
                self.assertNotIn("No deployments available", error.detail)
                self.assertNotIn("No healthy deployment available", error.detail)

    def test_the_tiers_the_key_holds_are_served(self):
        request = {"metadata": {"user_api_key_auth": ROUTER_KEY}}
        self.assertEqual(self.filtered(request, [SMALL], "small"), [SMALL])

    def test_the_live_router_lists_every_group_of_the_model(self):
        calls = []
        sys.modules[PROXY_SERVER] = live_router(["cetirun-model-cccc"], calls)
        self.assertEqual(self.refused({"metadata": {"user_api_key_auth": ROUTER_KEY}}).detail, REFUSAL)
        self.assertEqual(calls, [("large", "cetirun-team-synthetic")])

    def test_a_tier_the_key_holds_whose_deployments_are_unavailable_is_left_to_litellm(self):
        # The key may use large, but its deployment in the key's group was
        # removed for cooldown before the callback ran; LiteLLM reports that.
        key = Key(["cetirun-model-cccc"], team_id="cetirun-team-synthetic")
        sys.modules[PROXY_SERVER] = live_router(["cetirun-model-cccc"])
        self.assertEqual(self.filtered({"metadata": {"user_api_key_auth": key}}, [LARGE_CONSOLE], "large"), [])
        self.assertEqual(self.filtered({"metadata": {"user_api_key_auth": key}}, [], "large"), [])

    def test_without_the_live_router_the_deployments_given_decide(self):
        class FailingRouter:
            def get_model_access_groups(self, **kwargs):
                raise RuntimeError("synthetic")

        key = Key(["cetirun-model-cccc"])
        for router in (None, FailingRouter()):
            with self.subTest(router=router):
                proxy = types.ModuleType(PROXY_SERVER)
                proxy.llm_router = router
                sys.modules[PROXY_SERVER] = proxy
                self.refused({"metadata": {"user_api_key_auth": key}}, [LARGE_CONSOLE])

    def test_the_routers_classifier_call_is_exempt(self):
        # The router calls its classifier model on the key's behalf, and the
        # key need not hold it. LiteLLM marks the call in either bucket; the
        # proxy drops the field from the metadata a client sends.
        for key_bucket, origin_bucket in (("metadata", "metadata"), ("litellm_metadata", "litellm_metadata"),
                                          ("metadata", "litellm_metadata")):
            with self.subTest(key_bucket=key_bucket, origin_bucket=origin_bucket):
                request = {key_bucket: {"user_api_key_auth": ROUTER_KEY}}
                request.setdefault(origin_bucket, {})["internal_call_origin"] = "autorouter_classifier"
                self.assertEqual(self.filtered(request, [LARGE], "large"), [LARGE])

    def test_no_other_call_origin_is_exempt(self):
        for origin in ("autorouter_classifier ", "AUTOROUTER_CLASSIFIER", "health_check", None,
                       ["autorouter_classifier"]):
            with self.subTest(origin=origin):
                self.refused({"metadata": {"user_api_key_auth": ROUTER_KEY, "internal_call_origin": origin}})

    def test_the_no_access_placeholder_is_an_operator_key(self):
        self.refused({"metadata": {"user_api_key_auth": Key(["no-default-models"])}})

    def test_other_keys_are_left_to_litellm(self):
        # The master key (no models), keys granted every model, "*", models by
        # name, or anything besides the operator's groups.
        for models in ([], None, ["*"], ["all-proxy-models"], ["large"], ["cetirun-model-aaaa", "large"],
                       ["CETIRUN-MODEL-AAAA"], ["cetirun-model-aaaa", "*"]):
            with self.subTest(models=models):
                request = {"metadata": {"user_api_key_auth": Key(models)}}
                self.assertEqual(self.filtered(request, [LARGE, LARGE_CONSOLE], "large"), [LARGE, LARGE_CONSOLE])

    def test_only_the_authenticated_key_object_counts(self):
        for caller in ({"models": ["cetirun-model-aaaa"]}, types.SimpleNamespace(models=["cetirun-model-aaaa"])):
            with self.subTest(caller=caller):
                request = {"metadata": {"user_api_key_auth": caller}}
                self.assertEqual(self.filtered(request, [LARGE, LARGE_CONSOLE], "large"), [LARGE, LARGE_CONSOLE])

    def test_requests_without_a_key_are_left_to_litellm(self):
        for request_kwargs in (None, {}, {"metadata": {}}, {"metadata": {"user_api_key_auth": None}}):
            with self.subTest(request_kwargs=request_kwargs):
                self.assertEqual(self.filtered(request_kwargs, [LARGE, LARGE_CONSOLE], "large"), [LARGE, LARGE_CONSOLE])

    def test_when_the_copilot_rule_leaves_nothing_litellm_answers_as_before(self):
        # Only another Agent's route, or nothing at all: LiteLLM's own error.
        request = {"metadata": {"user_api_key_auth": Key(["cetirun-model-aaaa"])}}
        self.assertEqual(self.filtered(request, [BOB]), [])
        self.assertEqual(self.filtered(request, []), [])

    def test_a_refusal_is_logged_once_without_key_material(self):
        error = self.module.ModelRefused("large")
        with self.assertLogs(LOGGER, level="WARNING") as logs:
            self.assertIsNone(asyncio.run(self.guard.async_post_call_failure_hook({}, error, ROUTER_KEY)))
        self.assertEqual(len(logs.records), 1)
        self.assertIn("large", logs.output[0])
        self.assertIn("cetirun-synthetic-agent-0000", logs.output[0])
        self.assertNotIn("synthetic-hashed-token", logs.output[0])
        # The caller gets the error as it was raised.
        self.assertEqual((error.status_code, error.detail), (403, REFUSAL))

    def test_other_errors_are_not_logged(self):
        for error in (RouterRateLimitError(COOLDOWN_MESSAGE), HTTPException(403, REFUSAL), ProviderError("x")):
            with self.subTest(error=error), self.assertNoLogs(LOGGER, level="WARNING"):
                asyncio.run(self.guard.async_post_call_failure_hook({}, error, ROUTER_KEY))

    # ----- sampling parameters a router's chosen model refuses -----

    def with_gpt5_rule(self, none_effort=()):
        """LiteLLM's GPT-5 rule, as 1.105.1 states it, for model names; none_effort supports and defaults to none."""

        class OpenAIGPT5Config:
            @classmethod
            def is_model_gpt_5_model(cls, model):
                return ("gpt-5" in model or "gpt-6" in model) and "gpt-5-chat" not in model

            @classmethod
            def _supports_reasoning_effort_level(cls, model, level):
                return level == "none" and model in none_effort

            @classmethod
            def effort_resolves_to_none(cls, model, effort):
                return effort == "none" if effort is not None else model in none_effort

        names = ("litellm.llms", "litellm.llms.openai", "litellm.llms.openai.chat",
                 "litellm.llms.openai.chat.gpt_5_transformation")
        stubs = {name: types.ModuleType(name) for name in names}
        stubs[names[-1]].OpenAIGPT5Config = OpenAIGPT5Config
        saved = {name: sys.modules.get(name) for name in stubs}
        sys.modules.update(stubs)

        def restore():
            for name, previous in saved.items():
                if previous is None:
                    sys.modules.pop(name, None)
                else:
                    sys.modules[name] = previous

        self.addCleanup(restore)

    @staticmethod
    def model_deployment(name, model, base_model=None):
        info = {"id": f"d-{name}", "access_groups": ["cetirun-shared"]}
        if base_model:
            info["base_model"] = base_model
        return {"model_name": name, "litellm_params": {"model": model}, "model_info": info}

    @staticmethod
    def routed(router="auto", bucket="metadata", **params):
        return {bucket: {"user_api_key_auth": Key(["cetirun-shared"]),
                         "routing_decision": {"router_model_name": router, "tier": "MEDIUM"}}, **params}

    def test_a_router_tier_that_reasons_gets_no_sampling_parameters(self):
        self.with_gpt5_rule()
        azure = self.model_deployment("gpt-5.6-terra", "azure/gpt-5.6-terra")
        # A Copilot route behind the Gateway: the proxy applies the same rule.
        copilot = self.model_deployment("gpt-6-sol", "litellm_proxy/gpt-6-sol")
        for bucket in ("metadata", "litellm_metadata"):
            for tier, deployments in (("gpt-5.6-terra", [azure]), ("gpt-6-sol", [copilot])):
                with self.subTest(bucket=bucket, tier=tier):
                    request = self.routed(bucket=bucket, temperature=0, top_p=0.5, logprobs=True, top_logprobs=2,
                                          max_tokens=16)
                    with self.assertLogs(LOGGER, level="WARNING") as logs:
                        kept = self.filtered(request, deployments, tier)
                    self.assertEqual(kept, deployments)
                    for name in ("temperature", "top_p", "logprobs", "top_logprobs"):
                        self.assertNotIn(name, request)
                    self.assertEqual(request["max_tokens"], 16)
                    self.assertIn("auto", logs.output[0])

    def test_temperature_one_and_models_that_take_sampling_stay(self):
        self.with_gpt5_rule(none_effort=("gpt-5.4-nano",))
        plain = self.model_deployment("nano-plain", "openai/gpt-4.1-nano")
        # A deployment named after no model, priced through base_model.
        named = self.model_deployment("small", "azure/my-small", base_model="azure/gpt-4.1-mini")
        # Defaults to effort none: LiteLLM accepts any temperature.
        none_default = self.model_deployment("gpt-5.4-nano", "azure/gpt-5.4-nano")
        terra = self.model_deployment("gpt-5.6-terra", "azure/gpt-5.6-terra")
        for deployments, params in (([plain], {"temperature": 0, "top_p": 0.5}), ([named], {"temperature": 0}),
                                    ([none_default], {"temperature": 0}), ([terra], {"temperature": 1}),
                                    ([terra], {"temperature": None})):
            with self.subTest(deployments=deployments[0]["model_name"], params=params):
                request = self.routed(**params)
                self.filtered(request, deployments, deployments[0]["model_name"])
                for name, value in params.items():
                    self.assertEqual(request[name], value)

    def test_an_effort_of_none_keeps_sampling(self):
        self.with_gpt5_rule(none_effort=("gpt-5.6-terra",))
        terra = self.model_deployment("gpt-5.6-terra", "azure/gpt-5.6-terra")
        for effort in ({"reasoning_effort": "none"}, {"reasoning_effort": {"effort": "none"}}, {"reasoning": {"effort": "none"}}):
            with self.subTest(effort=effort):
                request = self.routed(temperature=0, **effort)
                self.filtered(request, [terra], "gpt-5.6-terra")
                self.assertEqual(request["temperature"], 0)
        request = self.routed(temperature=0, reasoning={"effort": "high"})
        self.filtered(request, [terra], "gpt-5.6-terra")
        self.assertNotIn("temperature", request)

    def test_one_deployment_that_refuses_is_enough(self):
        # Which deployment serves the request is chosen after this.
        self.with_gpt5_rule()
        deployments = [self.model_deployment("mixed", "openai/gpt-4.1"), self.model_deployment("mixed", "azure/gpt-6-sol")]
        request = self.routed(temperature=0.2)
        self.filtered(request, deployments, "mixed")
        self.assertNotIn("temperature", request)

    def test_a_model_asked_for_by_name_keeps_its_sampling_parameters(self):
        # The caller chose the model: LiteLLM's refusal tells it why.
        self.with_gpt5_rule()
        terra = self.model_deployment("gpt-5.6-terra", "azure/gpt-5.6-terra")
        for request in ({"metadata": {"user_api_key_auth": Key(["cetirun-shared"])}, "temperature": 0},
                        # The router's own selection, before it picks a tier.
                        self.routed(router="gpt-5.6-terra", temperature=0),
                        {"metadata": {"routing_decision": None}, "temperature": 0},
                        {"metadata": {"routing_decision": {"router_model_name": ""}}, "temperature": 0}):
            with self.subTest(request=request):
                self.filtered(request, [terra], "gpt-5.6-terra")
                self.assertEqual(request["temperature"], 0)

    def test_without_litellms_rule_nothing_is_removed(self):
        terra = self.model_deployment("gpt-5.6-terra", "azure/gpt-5.6-terra")
        request = self.routed(temperature=0)
        self.filtered(request, [terra], "gpt-5.6-terra")
        self.assertEqual(request["temperature"], 0)

    # ----- encrypted reasoning without the Gateway's mark -----

    def responses_request(self, *items):
        return {"model": "gpt-5", "input": [{"role": "user", "content": "hi"}, *[dict(item) for item in items]],
                "litellm_metadata": {"user_api_key_auth": Key(["cetirun-shared"])}}

    def with_affinity(self, *callbacks):
        self.module.litellm.callbacks = list(callbacks)
        self.addCleanup(setattr, self.module.litellm, "callbacks", [])

    def test_unmarked_encrypted_reasoning_is_removed_while_affinity_is_on(self):
        self.with_affinity(object(), EncryptedContentAffinityCheck())
        request = self.responses_request(UNMARKED, MARKED_CONTENT, MARKED_ID, UNMARKED_UNREADABLE, *CONVERSATION_TAIL)
        items = request["input"]
        with self.assertLogs(LOGGER, "WARNING") as logs:
            kept = self.filtered(request, [SHARED])
        self.assertEqual(kept, [SHARED])
        # The same list: LiteLLM forwards it, and its fallback hops share it.
        self.assertIs(request["input"], items)
        self.assertEqual(items, [
            {"role": "user", "content": "hi"},
            {"type": "reasoning", "summary": [{"type": "summary_text", "text": "Worked it out."}]},
            MARKED_CONTENT, MARKED_ID, *CONVERSATION_TAIL,
        ])
        self.assertIn("2 reasoning items without", logs.output[0])

    def test_reasoning_is_removed_once(self):
        self.with_affinity(EncryptedContentAffinityCheck())
        request = self.responses_request(UNMARKED)
        with self.assertLogs(LOGGER, "WARNING") as logs:
            self.filtered(request, [SHARED])
        self.assertIn("1 reasoning item without", logs.output[0])
        once = [dict(item) for item in request["input"]]
        with self.assertNoLogs(LOGGER, "WARNING"):
            self.filtered(request, [SHARED])
        self.assertEqual(request["input"], once)

    def test_reasoning_is_left_alone_without_affinity(self):
        # Without affinity LiteLLM marks nothing, so every item is unmarked and
        # each deployment reads its own.
        for callbacks in ([], [EncryptedContentAffinityCheck(enabled=False)], [object()]):
            with self.subTest(callbacks=callbacks):
                self.with_affinity(*callbacks)
                request = self.responses_request(UNMARKED, UNMARKED_UNREADABLE)
                before = [dict(item) for item in request["input"]]
                self.filtered(request, [SHARED])
                self.assertEqual(request["input"], before)

    def test_requests_without_a_responses_input_are_left_alone(self):
        self.with_affinity(EncryptedContentAffinityCheck())
        for request in ({"model": "gpt-5", "messages": [{"role": "user", "content": "hi"}]},
                        {"model": "gpt-5", "input": "hi"}, None):
            with self.subTest(request=request):
                before = None if request is None else dict(request)
                self.filtered(request, [SHARED])
                self.assertEqual(request, before)

    # ----- the Gateway's form of the mark -----

    def test_the_mark_is_written_as_an_opaque_token_and_read_back(self):
        content = "gAAAAB" + "x-_Az09" * 40 + "=="
        marked = self.module.mark_encrypted_content(content, "cetirun-shared-7388db16-9e9d2e8f")
        # OpenClaw keeps encrypted reasoning only in this form; anything else
        # goes through its secret redaction, which shortens gAAAA... values.
        self.assertRegex(marked, r"^[A-Za-z0-9+/_-]+={0,2}$")
        self.assertTrue(marked.startswith("ctrnenc"))
        self.assertEqual(self.module.read_marked_content(marked), ("cetirun-shared-7388db16-9e9d2e8f", content))

    def test_reading_the_mark_falls_back_to_litellms_form(self):
        def legacy(content):
            if content.startswith("litellm_enc:"):
                return "from-legacy", content.split(";", 1)[1]
            return None, content

        self.assertEqual(self.module.read_marked_content("litellm_enc:abc;gAAAA", legacy), ("from-legacy", "gAAAA"))
        self.assertEqual(self.module.read_marked_content("gAAAAplain", legacy), (None, "gAAAAplain"))
        for broken in ("ctrnenc", "ctrnencx", "ctrnenc6x", "ctrnencffxgAAAA", "ctrnenc6162gAAAA", "ctrnenc6162x"):
            with self.subTest(broken=broken):
                self.assertEqual(self.module.read_marked_content(broken), (None, broken))

    def test_litellm_writes_the_gateways_form_once_installed(self):
        class ResponsesAPIRequestUtils:
            @staticmethod
            def _wrap_encrypted_content_with_model_id(encrypted_content, model_id):
                return f"litellm_enc:{model_id};{encrypted_content}"

            @staticmethod
            def _unwrap_encrypted_content_with_model_id(wrapped_content):
                if not wrapped_content.startswith("litellm_enc:"):
                    return None, wrapped_content
                model_id, content = wrapped_content[len("litellm_enc:"):].split(";", 1)
                return model_id, content

        utils = types.ModuleType("litellm.responses.utils")
        utils.ResponsesAPIRequestUtils = ResponsesAPIRequestUtils
        responses = types.ModuleType("litellm.responses")
        stubs = {"litellm.responses": responses, "litellm.responses.utils": utils}
        saved = {name: sys.modules.get(name) for name in stubs}
        sys.modules.update(stubs)
        try:
            self.assertTrue(self.module.install_mark_form())
            self.assertTrue(self.module.install_mark_form())  # a second install changes nothing
        finally:
            for name, previous in saved.items():
                if previous is None:
                    sys.modules.pop(name, None)
                else:
                    sys.modules[name] = previous
        wrapped = ResponsesAPIRequestUtils._wrap_encrypted_content_with_model_id("gAAAAraw", "dep-1")
        self.assertTrue(wrapped.startswith("ctrnenc"))
        unwrap = ResponsesAPIRequestUtils._unwrap_encrypted_content_with_model_id
        self.assertEqual(unwrap(wrapped), ("dep-1", "gAAAAraw"))
        # Items marked before the change still route and are unwrapped.
        self.assertEqual(unwrap("litellm_enc:dep-2;gAAAAold"), ("dep-2", "gAAAAold"))
        self.assertEqual(unwrap("gAAAAplain"), (None, "gAAAAplain"))

    def test_items_in_the_gateways_form_are_marked(self):
        self.with_affinity(EncryptedContentAffinityCheck())
        item = {"type": "reasoning", "summary": [], "encrypted_content": self.module.mark_encrypted_content("gAAAAraw", "dep-1")}
        request = self.responses_request(item)
        with self.assertNoLogs(LOGGER, "WARNING"):
            self.filtered(request, [SHARED])
        self.assertEqual(request["input"][1], item)

    def test_shortened_encrypted_reasoning_is_removed(self):
        # A client's redacted copy: marked, but no deployment can read it.
        self.with_affinity(EncryptedContentAffinityCheck())
        shortened = self.module.mark_encrypted_content("gAAAAB\u2026ab12", "dep-1")
        request = self.responses_request(
            {"type": "reasoning", "summary": [], "content": [], "encrypted_content": shortened},
            {"type": "reasoning", "summary": [{"type": "summary_text", "text": "Kept."}], "encrypted_content": "litellm_enc:bWE=;gAAAAB\u2026ab12"},
        )
        with self.assertLogs(LOGGER, "WARNING") as logs:
            self.filtered(request, [SHARED])
        self.assertIn("2 reasoning items without", logs.output[0])
        self.assertEqual(request["input"][1:], [{"type": "reasoning", "summary": [{"type": "summary_text", "text": "Kept."}]}])

    # ----- no fallback after unreadable encrypted reasoning -----

    def fallback_request(self, error, depth=1, bucket="litellm_metadata"):
        return {"model": "gpt-5", "input": [{"role": "user", "content": "hi"}],
                bucket: {"user_api_key_auth": Key(["cetirun-shared"]), "attempted_fallbacks": depth,
                         "previous_models": [{"model_group": "gpt-6", "exception_type": "BadRequestError",
                                              "exception_string": error}]}}

    def test_a_fallback_after_unreadable_reasoning_ends_with_the_first_error(self):
        refusal = ("litellm.BadRequestError: AzureException - The encrypted content gAAA...evLI could not be verified. "
                   "Reason: Encrypted content could not be decrypted or parsed. code: invalid_encrypted_content")
        for bucket in ("litellm_metadata", "metadata"):
            with self.subTest(bucket=bucket):
                with self.assertLogs(LOGGER, "WARNING") as logs, self.assertRaises(Exception) as raised:
                    self.filtered(self.fallback_request(refusal, bucket=bucket), [SHARED])
                self.assertIn(refusal, str(raised.exception))
                self.assertIn("no fallback to copilot/gpt-5", logs.output[0])

    def test_the_refusal_is_a_bad_request_when_litellm_has_one(self):
        class BadRequestError(Exception):
            def __init__(self, message, model, llm_provider):
                super().__init__(message)
                self.status_code = 400

        self.module.litellm.BadRequestError = BadRequestError
        self.addCleanup(delattr, self.module.litellm, "BadRequestError")
        with self.assertLogs(LOGGER, "WARNING"), self.assertRaises(BadRequestError) as raised:
            self.filtered(self.fallback_request("invalid_encrypted_content"), [SHARED])
        self.assertEqual(raised.exception.status_code, 400)

    def test_other_fallbacks_go_on(self):
        for request in (self.fallback_request("litellm.InternalServerError: 500"),
                        self.fallback_request("invalid_encrypted_content", depth=0),
                        {"model": "gpt-5", "litellm_metadata": {"previous_models": "invalid_encrypted_content"}}):
            with self.subTest(request=request):
                self.assertEqual(self.filtered(request, [SHARED]), [SHARED])

    # ----- errors -----

    def test_the_cooldown_list_is_removed_from_the_error(self):
        error = RouterRateLimitError(COOLDOWN_MESSAGE)
        result = asyncio.run(self.guard.async_post_call_failure_hook({}, error, None))
        self.assertIsNone(result)
        message = str(error)
        self.assertNotIn("cooldown_list", message)
        self.assertNotIn("cetirun-copilot-bob", message)
        self.assertTrue(message.startswith("No deployments available for selected model, Try again in 5.0 seconds."))
        self.assertTrue(message.endswith("Passed model=copilot/gpt-5. pre-call-checks=False"))
        # LiteLLM derives retry-after from these, after the hook.
        self.assertIsInstance(error, RouterRateLimitError)
        self.assertEqual(error.cooldown_time, 5.0)

    def test_a_message_attribute_is_cleaned_as_well(self):
        error = ProviderError("litellm.RateLimitError: " + COOLDOWN_MESSAGE + "\nReceived Model Group=copilot/gpt-5")
        asyncio.run(self.guard.async_post_call_failure_hook({}, error, None))
        for text in (error.message, str(error)):
            self.assertNotIn("cooldown_list", text)
            self.assertIn("Try again in 5.0 seconds.", text)
            self.assertTrue(text.endswith("\nReceived Model Group=copilot/gpt-5"))
        self.assertEqual(error.status_code, 429)

    def test_every_list_in_a_message_is_removed(self):
        twice = COOLDOWN_MESSAGE + " Fallback error: " + COOLDOWN_MESSAGE.replace("copilot/gpt-5", "gpt-5")
        error = RouterRateLimitError(twice)
        asyncio.run(self.guard.async_post_call_failure_hook({}, error, None))
        self.assertNotIn("cooldown_list", str(error))
        self.assertEqual(str(error).count("Try again in 5.0 seconds."), 2)

    def test_an_empty_cooldown_list_is_removed(self):
        error = RouterRateLimitError(COOLDOWN_MESSAGE.split(", cooldown_list=")[0] + ", cooldown_list=[]")
        asyncio.run(self.guard.async_post_call_failure_hook({}, error, None))
        self.assertTrue(str(error).endswith("pre-call-checks=False"))

    def test_other_errors_are_left_alone(self):
        for message in ("Invalid model name passed in model=gpt-x", "cooldown_list=['a'] without the router's message",
                        "No deployments available for selected model, Try again in 5 seconds."):
            with self.subTest(message=message):
                error = ProviderError(message)
                asyncio.run(self.guard.async_post_call_failure_hook({}, error, None))
                self.assertEqual((str(error), error.message), (message, message))

    # ----- the model listing of keys the operator issued -----

    def listed(self, key, names):
        return asyncio.run(self.guard.async_filter_listed_models(key, list(names)))

    def listing_router(self, modes, fail=False):
        class Router:
            def get_model_list(self, model_name=None, team_id=None):
                if fail:
                    raise RuntimeError("synthetic")
                return [{"model_name": model_name, "model_info": ({"mode": mode} if mode is not None else {})}
                        for mode in modes.get(model_name, [])]

        proxy = types.ModuleType(PROXY_SERVER)
        proxy.llm_router = Router()
        sys.modules[PROXY_SERVER] = proxy

    def test_an_operator_key_lists_no_access_group_and_no_model_it_cannot_talk_to(self):
        self.listing_router({"gpt-6-sol": ["chat"], "auto": [None], "decision-1": ["evaluation"], "embedder": ["embedding"],
                             "mixed": ["embedding", "responses"], "copilot-only": [None]})
        key = Key(["cetirun-model-aaaa", "cetirun-copilot-alice", "cetirun-shared"])
        names = ["auto", "cetirun-copilot-alice", "decision-1", "embedder", "gpt-6-sol", "mixed", "copilot-only", "cetirun-shared"]
        self.assertEqual(self.listed(key, names), ["auto", "gpt-6-sol", "mixed", "copilot-only"])

    def test_other_callers_see_litellms_listing(self):
        self.listing_router({"decision-1": ["evaluation"]})
        for models in ([], ["*"], ["decision-1", "gpt-6-sol"], None):
            with self.subTest(models=models):
                names = ["decision-1", "cetirun-copilot-alice", "gpt-6-sol"]
                self.assertEqual(self.listed(Key(models), names), names)
        self.assertEqual(self.listed({"models": ["cetirun-shared"]}, ["cetirun-shared"]), ["cetirun-shared"])

    def test_without_a_router_only_the_key_groups_are_left_out(self):
        for fail in (False, True):
            with self.subTest(fail=fail):
                if fail:
                    self.listing_router({}, fail=True)
                else:
                    sys.modules.pop(PROXY_SERVER, None)
                key = Key(["cetirun-copilot-alice", "cetirun-model-aaaa"])
                self.assertEqual(self.listed(key, ["decision-1", "cetirun-copilot-alice"]), ["decision-1"])
    def test_module_exposes_the_registered_instance(self):
        self.assertIsInstance(self.module.handler, self.module.Guard)


if __name__ == "__main__":
    unittest.main()
