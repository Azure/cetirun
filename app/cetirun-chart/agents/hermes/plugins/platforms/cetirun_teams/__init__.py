# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""CetiRun Teams platform plugin — workload identity auth."""

def create_adapter(config):
    from .adapter import CetiRunTeamsAdapter

    return CetiRunTeamsAdapter(config)


def is_connected(config) -> bool:
    from gateway.platforms._shared import get_scoped_secret

    extra = getattr(config, "extra", {}) or {}
    return bool(get_scoped_secret("MSTEAMS_APP_ID", "") or extra.get("app_id"))


async def standalone_send(pconfig, chat_id, message, **kwargs):
    from .adapter import _standalone_send

    return await _standalone_send(pconfig, chat_id, message, **kwargs)


def register(ctx):
    ctx.register_platform(
        name="cetirun_teams",
        label="Microsoft Teams (Workload Identity)",
        adapter_factory=create_adapter,
        check_fn=lambda: True,
        validate_config=is_connected,
        is_connected=is_connected,
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
        standalone_sender_fn=standalone_send,
    )
