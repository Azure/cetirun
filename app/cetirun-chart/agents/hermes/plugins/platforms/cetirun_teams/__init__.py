# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""CetiRun Teams platform plugin — workload identity auth."""

from gateway.platform_registry import platform_registry, PlatformEntry
from .adapter import CetiRunTeamsAdapter, _standalone_send


def register(ctx=None):
    platform_registry.register(PlatformEntry(
        name="cetirun_teams",
        label="Microsoft Teams (Workload Identity)",
        adapter_factory=CetiRunTeamsAdapter,
        check_fn=lambda: True,
        required_env=["MSTEAMS_APP_ID"],
        allowed_users_env="CETIRUN_TEAMS_ALLOWED_USERS",
        # Register the cron home-channel env var so Hermes treats cetirun_teams
        # as a valid cron delivery target. Without it, _is_known_delivery_platform()
        # returns False and cron/cross-platform deliveries to a /sethome'd Teams
        # chat are silently dropped (cron/scheduler.py). The name matches the
        # fallback _home_target_env_var() already uses, so /sethome and the
        # no-home-channel notice read the same var.
        cron_deliver_env_var="CETIRUN_TEAMS_HOME_CHANNEL",
        # Out-of-process cron delivery via Bot Framework REST. Without this hook,
        # deliver=cetirun_teams cron jobs fail with "No live adapter for platform"
        # whenever the gateway runner isn't reachable in the cron process (e.g.
        # cron running separately, or mid-restart). The serviceUrl is read from
        # the state file learned from inbound webhooks, not pinned via env.
        standalone_sender_fn=_standalone_send,
    ))


register()
