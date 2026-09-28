#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Safely reconcile CetiRun's oauth2-proxy provider in the Istio mesh config."""

import argparse
import json
import os
import re
import subprocess
import tempfile


OAUTH2_PROXY_PROVIDER_NAME = re.compile(
    r"^[ \t]*-[ \t]+name:[ \t]*(?:oauth2-proxy|\"oauth2-proxy\"|'oauth2-proxy')(?:[ \t]+#.*)?[ \t]*$"
)
MAX_PATCH_ATTEMPTS = 5
OAUTH2_PROXY_PROVIDER = """- name: oauth2-proxy
  envoyExtAuthzHttp:
    service: oauth2-proxy.cetirun-istio.svc.cluster.local
    port: 4180
    includeRequestHeadersInCheck:
    - cookie
    - authorization
    - x-auth-request-redirect
    headersToUpstreamOnAllow:
    - x-auth-request-user
    - x-auth-request-email
    - x-auth-request-preferred-username
    - authorization
    headersToDownstreamOnDeny:
    - set-cookie
    - location
    statusOnError: "403"
"""
EXTENSION_PROVIDERS_ENTRY = re.compile(
    r"^extensionProviders:[ \t]*(.*?)(?:\r?\n)?$"
)


def ensure_header(mesh_text: str) -> tuple[str, bool]:
    lines = mesh_text.splitlines(keepends=True)
    provider_starts = [
        index
        for index, line in enumerate(lines)
        if OAUTH2_PROXY_PROVIDER_NAME.match(line.rstrip("\r\n"))
    ]
    if len(provider_starts) != 1:
        raise RuntimeError("oauth2-proxy extension provider was not resolved exactly")
    start = provider_starts[0]
    provider_indent = len(lines[start]) - len(lines[start].lstrip())
    end = len(lines)
    for index in range(start + 1, len(lines)):
        stripped = lines[index].lstrip()
        indent = len(lines[index]) - len(stripped)
        if indent == provider_indent and stripped.startswith("- name:"):
            end = index
            break
    ext_authz_lines = [
        index
        for index in range(start, end)
        if lines[index].strip() == "envoyExtAuthzHttp:"
    ]
    if len(ext_authz_lines) != 1:
        raise RuntimeError("oauth2-proxy ext_authz configuration is invalid")
    ext_authz = ext_authz_lines[0]
    ext_authz_indent = len(lines[ext_authz]) - len(lines[ext_authz].lstrip())
    ext_authz_end = end
    for index in range(ext_authz + 1, end):
        stripped = lines[index].lstrip()
        indent = len(lines[index]) - len(stripped)
        if stripped and indent <= ext_authz_indent:
            ext_authz_end = index
            break

    header_key = "headersToUpstreamOnAllow:"
    header_lines = [
        index
        for index in range(ext_authz + 1, ext_authz_end)
        if lines[index].lstrip().startswith(header_key)
    ]
    if len(header_lines) > 1:
        raise RuntimeError("oauth2-proxy upstream header configuration is invalid")
    if not header_lines:
        newline = "\r\n" if any(line.endswith("\r\n") for line in lines) else "\n"
        header_indent = " " * (ext_authz_indent + 2)
        item_indent = " " * (ext_authz_indent + 4)
        lines[ext_authz_end:ext_authz_end] = [
            f"{header_indent}headersToUpstreamOnAllow:{newline}",
            f"{item_indent}- authorization{newline}",
        ]
        return "".join(lines), True

    header = header_lines[0]
    header_indent = len(lines[header]) - len(lines[header].lstrip())
    header_value = lines[header].strip()
    if header_value == f"{header_key} []":
        newline = "\r\n" if lines[header].endswith("\r\n") else "\n"
        lines[header:header + 1] = [
            f"{' ' * header_indent}{header_key}{newline}",
            f"{' ' * (header_indent + 2)}- authorization{newline}",
        ]
        return "".join(lines), True
    if header_value != header_key:
        raise RuntimeError("oauth2-proxy upstream header configuration is invalid")

    insert_at = header + 1
    item_indent = " " * (header_indent + 2)
    for index in range(header + 1, ext_authz_end):
        stripped = lines[index].lstrip()
        indent = len(lines[index]) - len(stripped)
        if stripped.startswith("-"):
            # YAML permits an "indentless" sequence whose dash has the same
            # indentation as the containing mapping key.
            if indent < header_indent:
                break
            if stripped[1:].strip() == "authorization":
                return mesh_text, False
            item_indent = lines[index][:indent]
            insert_at = index + 1
            continue
        if stripped and indent <= header_indent:
            break
    lines.insert(insert_at, f"{item_indent}- authorization\n")
    return "".join(lines), True


def ensure_provider(mesh_text: str) -> tuple[str, bool]:
    provider_count = sum(
        1
        for line in mesh_text.splitlines()
        if OAUTH2_PROXY_PROVIDER_NAME.match(line)
    )
    if provider_count > 1:
        raise RuntimeError("oauth2-proxy extension provider was not resolved exactly")
    if provider_count == 1:
        return ensure_header(mesh_text)

    newline = "\r\n" if "\r\n" in mesh_text else "\n"
    provider = OAUTH2_PROXY_PROVIDER.replace("\n", newline)
    lines = mesh_text.splitlines(keepends=True)
    keys = [
        (index, EXTENSION_PROVIDERS_ENTRY.match(line))
        for index, line in enumerate(lines)
        if EXTENSION_PROVIDERS_ENTRY.match(line)
    ]
    if len(keys) > 1:
        raise RuntimeError("Istio extensionProviders configuration is ambiguous")
    if keys:
        index, match = keys[0]
        value = match.group(1).strip()
        if value and not value.startswith("#"):
            empty_list = re.fullmatch(r"\[\](?:[ \t]+(#.*))?", value)
            if not empty_list:
                raise RuntimeError(
                    "Istio extensionProviders inline configuration is unsupported"
                )
            comment = empty_list.group(1)
            suffix = f" {comment}" if comment else ""
            lines[index] = f"extensionProviders:{suffix}{newline}"
        lines.insert(index + 1, provider)
        updated = "".join(lines)
    else:
        separator = "" if not mesh_text or mesh_text.endswith(("\n", "\r")) else newline
        updated = f"{mesh_text}{separator}extensionProviders:{newline}{provider}"
    reconciled, _ = ensure_header(updated)
    return reconciled, True


def configmap_patch(configmap: dict) -> tuple[dict, bool]:
    resource_version = configmap.get("metadata", {}).get("resourceVersion")
    if not isinstance(resource_version, str) or not resource_version:
        raise RuntimeError("Istio ConfigMap resourceVersion is unavailable")
    mesh, changed = ensure_provider(configmap.get("data", {}).get("mesh", ""))
    return {
        "metadata": {"resourceVersion": resource_version},
        "data": {"mesh": mesh},
    }, changed


def reconcile_configmap(namespace: str, name: str, runner=subprocess.run) -> None:
    for attempt in range(MAX_PATCH_ATTEMPTS):
        raw = runner(
            [
                "kubectl", "get", "configmap", name, "--namespace", namespace,
                "--ignore-not-found", "-o", "json",
            ],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        if getattr(raw, "returncode", 0) != 0:
            raise RuntimeError("Istio ConfigMap could not be read safely")
        if not raw.stdout.strip():
            mesh, _ = ensure_provider("")
            document = {
                "apiVersion": "v1",
                "kind": "ConfigMap",
                "metadata": {"name": name, "namespace": namespace},
                "data": {"mesh": mesh},
            }
            descriptor, filename = tempfile.mkstemp(
                prefix="cetirun-istio-mesh-create-", suffix=".json"
            )
            try:
                os.fchmod(descriptor, 0o600)
                with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
                    json.dump(document, handle)
                result = runner(
                    ["kubectl", "create", "--filename", filename],
                    check=False,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.PIPE,
                    text=True,
                )
            finally:
                try:
                    os.unlink(filename)
                except FileNotFoundError:
                    pass
            if result.returncode == 0:
                return
            if "AlreadyExists" in result.stderr and attempt + 1 < MAX_PATCH_ATTEMPTS:
                continue
            raise RuntimeError("Istio ConfigMap could not be created safely")
        patch, changed = configmap_patch(json.loads(raw.stdout))
        if not changed:
            return
        descriptor, filename = tempfile.mkstemp(prefix="cetirun-istio-mesh-", suffix=".json")
        try:
            os.fchmod(descriptor, 0o600)
            with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
                json.dump(patch, handle)
            result = runner(
                ["kubectl", "patch", "configmap", name, "--namespace", namespace, "--type", "merge", "--patch-file", filename],
                check=False,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
            )
        finally:
            try:
                os.unlink(filename)
            except FileNotFoundError:
                pass
        if result.returncode == 0:
            return
        if (
            not any(reason in result.stderr for reason in ("Conflict", "NotFound"))
            or attempt + 1 == MAX_PATCH_ATTEMPTS
        ):
            raise RuntimeError("Istio ConfigMap could not be updated safely")
    raise AssertionError("unreachable")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--name", required=True)
    args = parser.parse_args()
    reconcile_configmap(args.namespace, args.name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
