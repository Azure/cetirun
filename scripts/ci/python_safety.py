# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Pytest-only Hermes interface doubles and an outbound-network/credential guard.

These tests cover the real auth service and Teams adapter logic, not Hermes
integration, Azure token acquisition, or live Bot Framework/JWKS endpoints.
"""

import ipaddress
import os
import socket
import sys
from types import ModuleType
from unittest.mock import patch


class UnusedHermesInterface:
    def __init__(self, *args, **kwargs):
        raise AssertionError("This unit suite does not implement the Hermes runtime")


class NoCloudCredential:
    def get_token(self, *args, **kwargs):
        raise AssertionError(
            "Cloud credential acquisition is forbidden in offline unit tests"
        )


class PlatformEntry:
    def __init__(self, **kwargs):
        self.__dict__.update(kwargs)


class Registry:
    def register(self, entry):
        if entry.name != "cetirun_teams" or not callable(entry.standalone_sender_fn):
            raise AssertionError("Unexpected Teams plugin registration")


def hermes_modules():
    modules = {}
    for name in (
        "gateway",
        "gateway.platforms",
        "gateway.platforms._shared",
        "gateway.config",
        "gateway.platform_registry",
    ):
        modules[name] = ModuleType(name)
    modules["gateway"].__path__ = []
    modules["gateway.platforms"].__path__ = []
    modules["gateway.platforms"].__dict__.update(
        BasePlatformAdapter=UnusedHermesInterface,
        MessageEvent=UnusedHermesInterface,
        SendResult=UnusedHermesInterface,
    )
    modules["gateway.platforms._shared"].get_scoped_secret = (
        lambda name, default=None, **_kwargs: os.environ.get(name, default)
    )
    modules["gateway.config"].Platform = str
    modules["gateway.platform_registry"].__dict__.update(
        PlatformEntry=PlatformEntry,
        platform_registry=Registry(),
    )
    return modules


def guard_connect(original):
    def connect(sock, address):
        if sock.family in (socket.AF_INET, socket.AF_INET6):
            try:
                allowed = ipaddress.ip_address(address[0]).is_loopback
            except ValueError:
                allowed = False
            if not allowed:
                raise AssertionError(
                    f"Outbound network is forbidden in unit tests: {address[0]}"
                )
        return original(sock, address)

    return connect


def guard_resolve(original):
    def getaddrinfo(host, *args, **kwargs):
        if host != "localhost":
            try:
                allowed = ipaddress.ip_address(host).is_loopback
            except ValueError:
                allowed = False
            if not allowed:
                raise AssertionError(
                    f"External DNS resolution is forbidden in unit tests: {host}"
                )
        return original(host, *args, **kwargs)

    return getaddrinfo


def pytest_configure(config):
    # Patch before collection imports the plugin and its registration entrypoint.
    patches = [
        patch.dict(sys.modules, hermes_modules()),
        patch("azure.identity.DefaultAzureCredential", NoCloudCredential),
        patch("socket.socket.connect", guard_connect(socket.socket.connect)),
        patch("socket.socket.connect_ex", guard_connect(socket.socket.connect_ex)),
        patch("socket.getaddrinfo", guard_resolve(socket.getaddrinfo)),
    ]
    for item in patches:
        item.start()
        config.add_cleanup(item.stop)
