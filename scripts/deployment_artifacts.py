#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Resolve an immutable CetiRun release from the public MCR registry.

This module deliberately does not invoke Docker or Helm.  It downloads the
exact chart blobs selected by the registry manifest and records image and
chart digests for a deployment lock.  Callers should keep the returned lock
private when it contains local paths.
"""

import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
import yaml


REGISTRY = "mcr.microsoft.com"
PREFIX = "aks/ai-runtime/cetirun"
_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
_VERSION = re.compile(r"^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$")
_SHA = re.compile(r"^[0-9a-f]{40}$")
_NAME = re.compile(r"^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$")
_MAX_MANIFEST = 2 * 1024 * 1024
_MAX_CONFIG = 16 * 1024 * 1024
_MAX_CHART = 100 * 1024 * 1024
_MAX_UNCOMPRESSED_CHART = 400 * 1024 * 1024
CHART_DIGEST_LABEL = "io.cetirun.release.chart-digests"


class ArtifactError(ValueError):
    """The selected release is absent, inconsistent, or unsafe to deploy."""

    artifact = None
    artifact_kind = None


def _request(url, *, accept=None, max_bytes=_MAX_MANIFEST):
    headers = {"User-Agent": "cetirun-deployment/1", "Accept": accept or "application/json"}
    try:
        with urlopen(Request(url, headers=headers), timeout=30) as response:
            body = response.read(max_bytes + 1)
            if len(body) > max_bytes:
                raise ArtifactError(f"registry response exceeds the bounded size for {url}")
            return response.headers, body
    except HTTPError as exc:
        raise ArtifactError(f"registry request failed with HTTP {exc.code}") from None
    except (URLError, TimeoutError) as exc:
        raise ArtifactError(f"registry request failed: {exc.__class__.__name__}") from None


def _json(url, *, accept=None):
    _, body = _request(url, accept=accept)
    try:
        return json.loads(body)
    except (ValueError, UnicodeDecodeError) as exc:
        raise ArtifactError(f"registry returned invalid JSON for {url}") from exc


def _manifest(repository, reference, kind="image"):
    url = f"https://{REGISTRY}/v2/{repository}/manifests/{reference}"
    accept = ", ".join(("application/vnd.oci.image.index.v1+json",
                         "application/vnd.docker.distribution.manifest.list.v2+json",
                         "application/vnd.oci.image.manifest.v1+json",
                         "application/vnd.docker.distribution.manifest.v2+json",
                         "application/vnd.cncf.helm.chart.content.v1.tar+gzip"))
    requested_digest = reference if _DIGEST.fullmatch(reference) else None
    headers, body = _request(url, accept=accept)
    digest = headers.get("Docker-Content-Digest")
    actual = "sha256:" + hashlib.sha256(body).hexdigest()
    if not digest or not _DIGEST.fullmatch(digest) or digest != actual:
        raise ArtifactError(f"registry did not return a valid digest for {repository}:{reference}")
    if requested_digest and digest != requested_digest:
        raise ArtifactError(f"requested digest does not match the returned manifest for {repository}")
    media_type = headers.get("Content-Type", "").split(";", 1)[0].strip()
    accepted = {"image": {"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
                           "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"},
                "chart": {"application/vnd.oci.image.manifest.v1+json"}}
    if not media_type or media_type not in accepted[kind]:
        raise ArtifactError(f"unexpected {kind} manifest media type for {repository}:{reference}")
    try:
        document = json.loads(body)
    except (ValueError, UnicodeDecodeError) as exc:
        raise ArtifactError(f"registry returned invalid manifest for {repository}:{reference}") from exc
    if document.get("schemaVersion") != 2 or document.get("mediaType", media_type) != media_type:
        raise ArtifactError(f"invalid OCI manifest schema for {repository}:{reference}")
    return digest, document, body


def _descriptor(descriptor, label):
    if not isinstance(descriptor, dict) or not _DIGEST.fullmatch(descriptor.get("digest", "")):
        raise ArtifactError(f"{label} has no valid digest")
    if type(descriptor.get("size")) is not int or descriptor["size"] < 0 or descriptor["size"] > 5 * 1024 * 1024 * 1024:
        raise ArtifactError(f"{label} has no valid bounded size")


def _image(repository, version, source_sha):
    digest, manifest, _ = _manifest(repository, version)
    manifests = manifest.get("manifests", [])
    if manifests:
        for item in manifests:
            _descriptor(item, f"{repository} platform manifest")
        candidates = [m for m in manifests if m.get("platform", {}).get("os") == "linux"
                      and m.get("platform", {}).get("architecture") == "amd64"]
        if len(candidates) != 1 or not _DIGEST.fullmatch(candidates[0].get("digest", "")):
            raise ArtifactError(f"{repository}:{version} has no unique linux/amd64 manifest")
        platform_digest = candidates[0]["digest"]
        _, platform_manifest, platform_bytes = _manifest(repository, platform_digest, "image")
        if len(platform_bytes) != candidates[0]["size"]:
            raise ArtifactError("platform manifest descriptor size mismatch")
    else:
        platform_digest, platform_manifest = digest, manifest
    config = platform_manifest.get("config", {})
    _descriptor(config, f"{repository} config")
    if config.get("mediaType") not in ("application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"):
        raise ArtifactError("unexpected image config media type")
    for layer in platform_manifest.get("layers", []):
        _descriptor(layer, f"{repository} image layer")
    config_digest = config.get("digest", "")
    if not _DIGEST.fullmatch(config_digest):
        raise ArtifactError(f"{repository}:{version} has no valid config digest")
    # Digest identity and release provenance are both required. A mutable tag
    # without source/version labels cannot establish the selected release.
    _, config_bytes = _request(f"https://{REGISTRY}/v2/{repository}/blobs/{config_digest}", max_bytes=_MAX_CONFIG)
    actual_config = "sha256:" + hashlib.sha256(config_bytes).hexdigest()
    if actual_config != config_digest or len(config_bytes) != config["size"]:
        raise ArtifactError(f"config blob digest mismatch for {repository}:{version}")
    try:
        config_document = json.loads(config_bytes)
    except (ValueError, UnicodeDecodeError) as exc:
        raise ArtifactError(f"config blob is not valid JSON for {repository}:{version}") from exc
    if config_document.get("os") != "linux" or config_document.get("architecture") != "amd64":
        raise ArtifactError(f"config platform is not linux/amd64 for {repository}:{version}")
    labels = (config_document.get("config", {}) or {}).get("Labels", {}) or {}
    declared_sha = labels.get("org.opencontainers.image.revision") or labels.get("org.opencontainers.image.sourceRevision")
    declared_version = labels.get("org.opencontainers.image.version")
    if declared_sha != source_sha:
        raise ArtifactError(f"{repository}:{version} revision label does not match the selected source")
    if declared_version != version:
        raise ArtifactError(f"{repository}:{version} version label does not match the release")
    chart_digests = None
    if CHART_DIGEST_LABEL in labels:
        try:
            chart_digests = json.loads(labels[CHART_DIGEST_LABEL])
        except (ValueError, TypeError):
            raise ArtifactError("image chart digest receipt is not valid JSON") from None
        if (not isinstance(chart_digests, dict) or not chart_digests or
                any(not _NAME.fullmatch(name) or not isinstance(pin, str) or not _DIGEST.fullmatch(pin)
                    for name, pin in chart_digests.items())):
            raise ArtifactError("image chart digest receipt is invalid")
    return {"reference": f"{REGISTRY}/{repository}:{version}@{digest}", "digest": digest,
            "platformDigest": platform_digest, "configDigest": config_digest,
            "revision": declared_sha or None,
            "revisionVerified": bool(declared_sha), "versionVerified": bool(declared_version),
            "chartDigests": chart_digests}


def _safe_chart_members(path):
    try:
        with tarfile.open(path, "r:gz") as archive:
            seen = set()
            total = 0
            for count, member in enumerate(archive, start=1):
                target = Path(member.name)
                if count > 20000:
                    raise ArtifactError("chart archive has too many members")
                canonical = target.as_posix()
                if canonical in seen:
                    raise ArtifactError(f"chart archive contains duplicate member: {member.name}")
                seen.add(canonical)
                if member.size < 0 or member.size > _MAX_UNCOMPRESSED_CHART or total + member.size > _MAX_UNCOMPRESSED_CHART:
                    raise ArtifactError(f"chart archive exceeds the bounded decompressed size: {member.name}")
                total += member.size
                if (target.is_absolute() or ".." in target.parts or "\\" in member.name or member.issym() or member.islnk()
                        or member.isdev() or member.isfifo()):
                    raise ArtifactError(f"chart archive contains unsafe member: {member.name}")
    except tarfile.TarError as exc:
        raise ArtifactError(f"invalid chart archive: {path.name}") from exc


def _chart(repository, version, work, expected_archive_digest):
    digest, manifest, _ = _manifest(repository, version, "chart")
    chart_config = manifest.get("config", {})
    _descriptor(chart_config, f"{repository} chart config")
    if chart_config.get("mediaType") != "application/vnd.cncf.helm.config.v1+json":
        raise ArtifactError(f"{repository}:{version} has an invalid Helm config media type")
    layers = manifest.get("layers", [])
    if len(layers) != 1 or layers[0].get("mediaType") != "application/vnd.cncf.helm.chart.content.v1.tar+gzip":
        raise ArtifactError(f"{repository}:{version} does not contain exactly one Helm chart layer")
    layer = layers[0]
    _descriptor(layer, f"{repository} chart layer")
    if layer.get("digest") != expected_archive_digest:
        raise ArtifactError(f"chart archive differs from the source-verified release receipt: {repository}")
    if not _DIGEST.fullmatch(layer.get("digest", "")):
        raise ArtifactError(f"{repository}:{version} has no valid chart layer digest")
    _, body = _request(f"https://{REGISTRY}/v2/{repository}/blobs/{layer['digest']}",
                       max_bytes=_MAX_CHART,
                       accept="application/vnd.cncf.helm.chart.content.v1.tar+gzip")
    filename = repository.rsplit("/", 1)[-1] + f"-{version}.tgz"
    path = work / filename
    path.write_bytes(body)
    if hashlib.sha256(body).hexdigest() != layer["digest"].removeprefix("sha256:") or len(body) != layer["size"]:
        raise ArtifactError(f"chart layer digest mismatch for {repository}:{version}")
    _safe_chart_members(path)
    metadata = _chart_metadata(path, repository.rsplit("/", 1)[-1], version)
    return {"path": str(path), "digest": digest, "sha256": "sha256:" + hashlib.sha256(body).hexdigest(), **metadata}


def _chart_metadata(path, expected_name, expected_version):
    with tarfile.open(path, "r:gz") as archive:
        entries = [member for member in archive.getmembers() if member.name.endswith("/Chart.yaml")]
        if not entries:
            raise ArtifactError(f"chart {expected_name} has no Chart.yaml")
        root = min(entries, key=lambda member: len(Path(member.name).parts))
        if len(Path(root.name).parts) != 2 or Path(root.name).parts[0] != expected_name:
            raise ArtifactError(f"chart root path does not match {expected_name}")
        metadata = []
        for member in entries:
            try:
                text = archive.extractfile(member).read().decode("utf-8")
            except (AttributeError, UnicodeDecodeError) as exc:
                raise ArtifactError(f"chart {expected_name} has invalid Chart.yaml") from exc
            try:
                fields = yaml.safe_load(text)
            except yaml.YAMLError as exc:
                raise ArtifactError(f"chart {expected_name} has invalid Chart.yaml") from exc
            if not isinstance(fields, dict):
                raise ArtifactError(f"chart {expected_name} has invalid Chart.yaml")
            if not all(key in fields for key in ("name", "version", "appVersion")):
                raise ArtifactError(f"chart {expected_name} has incomplete Chart.yaml")
            if fields["version"] != expected_version or fields["appVersion"] != expected_version:
                raise ArtifactError(f"embedded chart metadata does not match {expected_name}:{expected_version}")
            metadata.append(fields)
    root_fields = metadata[entries.index(root)]
    if root_fields["name"] != expected_name:
        raise ArtifactError(f"chart metadata does not match {expected_name}:{expected_version}")
    embedded_by_name = {item["name"]: item for item in metadata if item is not root_fields}
    embedded_names = [item["name"] for item in metadata if item is not root_fields]
    if len(embedded_names) != len(set(embedded_names)):
        raise ArtifactError(f"chart contains duplicate embedded chart metadata: {expected_name}")
    for dependency in root_fields.get("dependencies", []) or []:
        if not isinstance(dependency, dict) or dependency.get("name") not in embedded_by_name:
            raise ArtifactError(f"chart dependency is missing from the package: {dependency}")
        if dependency.get("version") != embedded_by_name[dependency["name"]]["version"]:
            raise ArtifactError(f"chart dependency version is inconsistent: {dependency['name']}")
    dependencies = [{"name": item["name"], "version": item["version"]} for item in root_fields.get("dependencies", []) or []]
    if set(embedded_names) != {item["name"] for item in dependencies}:
        raise ArtifactError(f"chart dependency set is inconsistent: {expected_name}")
    return {"name": root_fields["name"], "version": root_fields["version"], "appVersion": root_fields["appVersion"],
            "embedded": list(embedded_by_name), "dependencies": dependencies}


validate_chart_archive = _safe_chart_members


def resolve(source: Path, work: Path, release_tag: str):
    """Resolve ``release_tag`` using inventory and source metadata in ``source``."""
    if not isinstance(release_tag, str) or not release_tag.startswith("v"):
        raise ArtifactError("release tag must start with v")
    tag = release_tag.removeprefix("v")
    if not _VERSION.fullmatch(tag):
        raise ArtifactError("release tag must be vX.Y.Z")
    source = Path(source).resolve()
    work = Path(work).resolve()
    inventory_path = source / "scripts/release-artifacts.json"
    chart_path = source / "app/cetirun-chart/Chart.yaml"
    if not inventory_path.is_file() or not chart_path.is_file():
        raise ArtifactError("selected source is missing release metadata")
    try:
        data = json.loads(inventory_path.read_text())
        chart = chart_path.read_text()
        dirty = subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=no"], cwd=source, text=True)
        if dirty.strip():
            raise ArtifactError("selected source has tracked working-tree changes")
        source_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=source, text=True).strip()
        tag_sha = subprocess.run(["git", "rev-parse", "--verify", f"refs/tags/{release_tag}^{{commit}}"], cwd=source,
                                 text=True, capture_output=True, check=False)
        if tag_sha.returncode != 0:
            raise ArtifactError("selected source does not contain the requested release tag")
        if tag_sha.stdout.strip() != source_sha:
            raise ArtifactError("release tag does not point at the selected source")
    except ArtifactError:
        raise
    except Exception as exc:
        raise ArtifactError("cannot read selected source metadata") from exc
    if not _SHA.fullmatch(source_sha) or not re.search(rf"^version:\s*['\"]?{re.escape(tag)}['\"]?\s*$", chart, re.M):
        raise ArtifactError("release version or source revision is inconsistent")
    if data.get("schemaVersion") != 1 or not isinstance(data.get("images"), list) or not isinstance(data.get("charts"), list):
        raise ArtifactError("unsupported artifact inventory schema")
    for kind in ("images", "charts"):
        names = [entry.get("name") for entry in data[kind] if isinstance(entry, dict)]
        if len(names) != len(data[kind]) or len(names) != len(set(names)) or any(not isinstance(name, str) or not _NAME.fullmatch(name) for name in names):
            raise ArtifactError(f"invalid or duplicate {kind} inventory name")
        for entry in data[kind]:
            if kind == "charts":
                chart_source = (source / entry.get("path", "")).resolve()
                if source not in chart_source.parents or not (chart_source / "Chart.yaml").is_file():
                    raise ArtifactError(f"invalid chart source path for {entry['name']}")
    work.mkdir(parents=True, exist_ok=True)
    images = {}
    for entry in data["images"]:
        try:
            images[entry["name"]] = _image(f"{PREFIX}/{entry['name']}", tag, source_sha)
        except ArtifactError as error:
            error.artifact, error.artifact_kind = entry["name"], "image"
            raise
    # This receipt is part of the Operator image config whose SHA256, source
    # revision and version were checked above. A tag replacement or partial MCR
    # synchronization cannot substitute an older chart with the same version.
    expected_chart_digests = images.get("cetirun-operator", {}).get("chartDigests")
    if not isinstance(expected_chart_digests, dict) or set(expected_chart_digests) != {entry["name"] for entry in data["charts"]}:
        raise ArtifactError("source-verified Operator image is missing the complete chart digest receipt")
    charts = {}
    for entry in data["charts"]:
        try:
            charts[entry["name"]] = _chart(f"{PREFIX}/helm/{entry['name']}", tag, work, expected_chart_digests[entry["name"]])
        except ArtifactError as error:
            error.artifact, error.artifact_kind = entry["name"], "chart"
            raise
        metadata = yaml.safe_load((source / entry["path"] / "Chart.yaml").read_text())
        expected = [{"name": item["name"], "version": item["version"]} for item in metadata.get("dependencies", []) or []]
        actual = charts[entry["name"]].get("dependencies", [])
        if sorted(expected, key=lambda item: item["name"]) != sorted(actual, key=lambda item: item["name"]):
            raise ArtifactError(f"published dependencies differ from the selected source: {entry['name']}")
    return {"schemaVersion": 1, "sourceSHA": source_sha, "version": tag,
            "registry": f"{REGISTRY}/{PREFIX}", "images": images, "charts": charts}
