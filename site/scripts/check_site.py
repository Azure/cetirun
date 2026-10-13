#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Validate a built CetiRun site without network, browser, or cloud access."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit

import tomllib

SITE = Path(__file__).resolve().parents[1]
MISSION = (
    "CetiRun is an open-source managed runtime for hosting AI agents "
    "and AI applications."
)
LOGO_SHA256 = "65f6e9aa51d5cbb43802c0ed91dd5a4fea891bc8a326cb6f091dffe1a5619791"
SECTIONS = (
    "overview",
    "getting-started",
    "concepts",
    "guides",
    "reference",
    "operations",
    "security",
    "contributing",
    "guidances",
    "user-guide",
)
MODULE_TITLES = ("Overview", "User Guide", "Operations", "Reference", "Contributing")
ROADMAP = "/docs/overview/roadmap"
POLICY_FILES = {
    "CONTRIBUTING.md",
    "DEVELOPMENT.md",
    "GOVERNANCE.md",
    "SUPPORT.md",
    "PRIVACY.md",
    "NOTICE",
}
POINTERS = {
    "docs/agent-harness-contract.md": "reference/agent-harness.md",
    "docs/agent-policy.md": "reference/crds.md",
    "docs/guides/msteams.md": "guides/msteams.md",
    "docs/guides/tailscale.md": "guides/tailscale.md",
    "docs/live-copilot-model-selection.design.md": "guides/models.md",
    "app/cetirun-chart/README.md": "reference/helm.md",
    "app/cetirun-operator/README.md": "reference/crds.md",
    "infra/README.md": "operations/infrastructure.md",
    "infra/storage/README.md": "operations/storage.md",
    "infra/istio/README.md": "operations/istio.md",
    "scripts/README.md": "reference/scripts.md",
}
DOC_ASSETS = {"docs/logo.jpg"}
TRADEMARK_GUIDELINES = "https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general"


class Document(HTMLParser):
    """Collect links and structural accessibility evidence from generated HTML."""

    def __init__(self, text: str) -> None:
        super().__init__(convert_charrefs=True)
        self.ids: set[str] = set()
        self.links: list[str] = []
        self.asset_links: list[str] = []
        self.search_links: list[str] = []
        self.headings: list[int] = []
        self.main_count = 0
        self.lang = ""
        self.title = ""
        self.description = ""
        self.in_title = False
        self.errors: list[str] = []
        self.label_targets: set[str] = set()
        self.inputs: list[dict[str, str | None]] = []
        self.nav_stack: list[str] = []
        self.sidebar_module = ""
        self.sidebar_links: list[str] = []
        self.sidebar_current: list[str] = []
        self.top_links: list[dict[str, str]] = []
        self.top_link: dict[str, str] | None = None
        self.feed(text)
        self.close()

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attr = dict(attrs)
        if tag == "nav":
            self.nav_stack.append(attr.get("data-nav-module") or "")
            if attr.get("data-nav-module"):
                self.sidebar_module = attr["data-nav-module"]
        if tag == "a":
            if any(self.nav_stack):
                self.sidebar_links.append(attr.get("href") or "")
                if attr.get("aria-current") == "page":
                    self.sidebar_current.append(attr.get("href") or "")
            if attr.get("data-nav-top"):
                self.top_link = {
                    "id": attr["data-nav-top"],
                    "href": attr.get("href") or "",
                    "text": "",
                    "current": attr.get("aria-current") or "",
                }
                self.top_links.append(self.top_link)
        identifier = attr.get("id")
        if identifier:
            if identifier in self.ids:
                self.errors.append(f"duplicate id: {identifier}")
            self.ids.add(identifier)
        if tag == "html":
            self.lang = attr.get("lang") or ""
        if tag == "main":
            self.main_count += 1
        if tag == "title":
            self.in_title = True
        if tag == "meta" and attr.get("name") == "description":
            self.description = attr.get("content") or ""
        if re.fullmatch(r"h[1-6]", tag):
            self.headings.append(int(tag[1]))
        if tag == "img" and "alt" not in attr:
            self.errors.append("image has no alt attribute")
        if tag == "input" and attr.get("type") not in {"hidden", "submit", "button"}:
            self.inputs.append(attr)
        if tag == "label" and attr.get("for"):
            self.label_targets.add(attr["for"])
        if tag in {"a", "link", "img", "script", "source"}:
            target = attr.get("href" if tag in {"a", "link"} else "src")
            if target:
                self.links.append(target)
                if tag in {"img", "script", "source"} or (
                    tag == "link"
                    and attr.get("rel") in {"stylesheet", "preload", "icon"}
                ):
                    self.asset_links.append(target)
        if target := attr.get("data-offline-search-index-json-src"):
            self.links.append(target)
            self.search_links.append(target)
        if tag == "meta" and attr.get("http-equiv", "").lower() == "refresh":
            self.errors.append(
                "unexpected redirect page; keep canonical pages navigable"
            )

    def handle_endtag(self, tag: str) -> None:
        if tag == "title":
            self.in_title = False
        if tag == "nav" and self.nav_stack:
            self.nav_stack.pop()
        if tag == "a":
            self.top_link = None

    def handle_data(self, text: str) -> None:
        if self.in_title:
            self.title += text
        if self.top_link is not None:
            self.top_link["text"] += text

    def accessibility_errors(self) -> list[str]:
        errors = list(self.errors)
        if not self.lang:
            errors.append("missing document language")
        if not self.title.strip() or not self.description.strip():
            errors.append("missing page title or description")
        if self.main_count != 1:
            errors.append(f"expected one main landmark, found {self.main_count}")
        if self.headings.count(1) != 1 or not self.headings or self.headings[0] != 1:
            errors.append("expected exactly one leading h1")
        for previous, current in zip(self.headings, self.headings[1:]):
            if current > previous + 1:
                errors.append(f"heading jump: h{previous} to h{current}")
        if "#main-content" not in self.links or "main-content" not in self.ids:
            errors.append("missing working skip-to-content link")
        for attr in self.inputs:
            if not (
                attr.get("aria-label")
                or attr.get("id") in self.label_targets
                or (
                    attr.get("aria-labelledby")
                    and set(attr["aria-labelledby"].split()).issubset(self.ids)
                )
            ):
                errors.append("input has no accessible label")
        return errors


def page_url(relative: Path, base_url: str) -> str:
    path = relative.as_posix()
    path = path.removesuffix("index.html")
    return urljoin(base_url, path)


def local_target(root: Path, url: str, base_url: str) -> Path | None:
    """Resolve same-origin URLs; reject subpath escapes, including encoded ones."""
    target, base = urlsplit(url), urlsplit(base_url)
    if target.scheme not in {"http", "https"}:
        raise ValueError(f"unsupported link scheme: {target.scheme}")
    if target.netloc != base.netloc:
        return None
    base_path = unquote(base.path).rstrip("/") + "/"
    path = unquote(target.path)
    if path == base_path.rstrip("/"):
        path += "/"
    if not path.startswith(base_path):
        raise ValueError(f"link outside site base path: {url}")
    relative = path[len(base_path) :]
    destination = (root / relative).resolve()
    if not destination.is_relative_to(root.resolve()):
        raise ValueError(f"link escapes output directory: {url}")
    if path.endswith("/") or not destination.suffix:
        destination /= "index.html"
    return destination


def navigation_pages(module: dict) -> list[str]:
    return [page for group in module["groups"] for page in group["pages"]]


def check_module_navigation(
    document: Document, relative: Path, base_url: str, modules: list[dict]
) -> list[str]:
    """Inspect emitted links, including hidden mobile links, not CSS visibility."""
    errors = []
    titles = [link["text"].strip() for link in document.top_links]
    if titles != [*MODULE_TITLES, "GitHub"]:
        errors.append("top navigation must be " + ", ".join((*MODULE_TITLES, "GitHub")))
    expected_top = {
        module["id"]: urljoin(base_url, module["page"].lstrip("/") + "/")
        for module in modules
    }
    expected_top["github"] = "https://github.com/Azure/cetirun"
    for link in document.top_links:
        if urljoin(base_url, link["href"]) != expected_top.get(link["id"]):
            errors.append(f"wrong top navigation destination: {link['id']}")

    page = "/" + relative.as_posix().removesuffix("/index.html").rstrip("/")
    matches = [
        module
        for module in modules
        if page in navigation_pages(module) + module.get("extra_paths", [])
    ]
    if not relative.is_relative_to("docs"):
        if any(link["current"] for link in document.top_links):
            errors.append("non-document page must not select a documentation module")
        return errors
    if len(matches) != 1:
        return [*errors, f"page must belong to one navigation module: {page}"]
    module = matches[0]
    if document.sidebar_module != module["id"]:
        errors.append(f"expected {module['id']} sidebar")
    expected = {
        urljoin(base_url, path.lstrip("/") + "/") for path in navigation_pages(module)
    }
    actual = [urljoin(base_url, link) for link in document.sidebar_links]
    if set(actual) - expected:
        errors.append("sidebar contains cross-module or unexpected links")
    if expected - set(actual):
        errors.append("sidebar is missing module pages")
    if len(actual) != len(set(actual)):
        errors.append("sidebar contains duplicate links")
    selected = module["page"] if page in module.get("extra_paths", []) else page
    if [urljoin(base_url, link) for link in document.sidebar_current] != [
        urljoin(base_url, selected.lstrip("/") + "/")
    ]:
        errors.append("sidebar must mark exactly the current page")
    if [link["id"] for link in document.top_links if link["current"]] != [module["id"]]:
        errors.append("top navigation must mark exactly the current module")
    return errors


def check_generated(root: Path, base_url: str) -> list[str]:
    errors: list[str] = []
    documents = {
        path.resolve(): Document(path.read_text(encoding="utf-8"))
        for path in root.rglob("*.html")
    }
    if not documents or (root / "index.html").resolve() not in documents:
        return ["no generated home page; build the site first"]
    search_paths: set[Path] = set()
    modules = tomllib.loads((SITE / "data/navigation.toml").read_text())["modules"]
    for path, document in documents.items():
        relative = path.relative_to(root.resolve())
        errors.extend(
            f"{relative}: {error}" for error in document.accessibility_errors()
        )
        errors.extend(
            f"{relative}: {error}"
            for error in check_module_navigation(document, relative, base_url, modules)
        )
        origin = page_url(relative, base_url)
        for target in document.links:
            if target.startswith(("mailto:", "tel:")):
                continue
            if target.startswith("data:") and target in document.asset_links:
                continue
            try:
                resolved = urljoin(origin, target)
                destination = local_target(root, resolved, base_url)
                if destination is None:
                    if target in document.asset_links:
                        errors.append(f"{relative}: external runtime asset: {target}")
                    continue
                if not destination.is_file():
                    errors.append(f"{relative}: missing target: {target}")
                fragment = unquote(urlsplit(resolved).fragment)
                if (
                    fragment
                    and destination in documents
                    and fragment not in documents[destination].ids
                ):
                    errors.append(f"{relative}: missing fragment: {target}")
                if target in document.search_links:
                    search_paths.add(destination)
            except ValueError as error:
                errors.append(f"{relative}: {error}")
    if not search_paths:
        errors.append("client-side search index is missing")
    indexed: set[Path] = set()
    for path in search_paths:
        try:
            for item in json.loads(path.read_text(encoding="utf-8")):
                destination = local_target(
                    root, urljoin(base_url, item["ref"]), base_url
                )
                if destination not in documents:
                    errors.append(f"search index has missing page: {item['ref']}")
                elif not item.get("title") or not item.get("description"):
                    errors.append(f"search index has missing metadata: {item['ref']}")
                else:
                    indexed.add(destination)
        except (OSError, ValueError, KeyError, TypeError) as error:
            errors.append(f"invalid search index: {error}")
    docs_root = (root / "docs").resolve()
    for path in documents:
        if path.is_relative_to(docs_root) and path not in indexed:
            errors.append(f"page missing from search index: {path.relative_to(root)}")
    home = documents[(root / "index.html").resolve()]
    docs = documents.get((root / "docs/index.html").resolve())
    if not docs:
        errors.append("documentation landing page missing")
    for target in ("docs/getting-started/", "docs/overview/", "docs/contributing/"):
        if not any(
            urljoin(base_url, link) == urljoin(base_url, target) for link in home.links
        ):
            errors.append(f"home reader path missing: {target}")
    return errors


def frontmatter(text: str) -> dict[str, str]:
    match = re.match(r"---\n(.*?)\n---\n", text, re.DOTALL)
    if not match:
        return {}
    return {
        key: value.strip().strip("\"'")
        for key, value in re.findall(r"^(\w+):\s*(.+)$", match[1], re.MULTILINE)
    }


def markdown_prose(text: str) -> str:
    """Ignore frontmatter and fenced examples when checking source links."""
    text = re.sub(r"\A---\n.*?\n---\n", "", text, count=1, flags=re.DOTALL)
    prose = []
    fence = ""
    for line in text.splitlines():
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", line)
        if fence:
            if (
                marker
                and marker[1][0] == fence[0]
                and len(marker[1]) >= len(fence)
                and not marker[2].strip()
            ):
                fence = ""
        elif marker:
            fence = marker[1]
        else:
            prose.append(line)
    return "\n".join(prose)


def markdown_anchors(text: str) -> set[str]:
    prose = markdown_prose(text)
    anchors = set(re.findall(r'<a\s+(?:id|name)=["\']([^"\']+)', prose))
    seen: dict[str, int] = {}
    for heading in re.findall(r"^#{1,6}\s+(.+?)\s*#*\s*$", prose, re.MULTILINE):
        slug = re.sub(r"[^\w -]", "", heading.lower()).replace(" ", "-")
        count = seen.get(slug, 0)
        anchors.add(f"{slug}-{count}" if count else slug)
        seen[slug] = count + 1
    return anchors


def check_doc_inventory(site: Path) -> list[str]:
    """Require an explicit destination or asset role for every docs/ file."""
    repo = site.parent
    directory = repo / "docs"
    if not directory.is_dir() or directory.is_symlink():
        return ["docs/ must be a real documentation directory"]
    errors = []
    classified = set(POINTERS) | DOC_ASSETS
    for path in sorted(directory.rglob("*")):
        relative = path.relative_to(repo).as_posix()
        if path.is_symlink():
            errors.append(f"documentation symlink requires review: {relative}")
        elif path.is_file() and relative not in classified:
            errors.append(f"unmapped documentation file: {relative}")
    return errors


def check_repository_links(repo: Path) -> list[str]:
    """Check Markdown entry points as repository paths, not rendered URLs."""
    repo = repo.resolve()
    sources = set(repo.glob("*.md")) | set((repo / "docs").rglob("*.md"))
    sources.update(
        repo / name for name in (*POINTERS, "app/README.md", "site/README.md")
    )
    errors = []
    for source in sorted(sources):
        if not source.is_file():
            continue
        relative = source.relative_to(repo)
        if not source.resolve().is_relative_to(repo):
            errors.append(f"{relative}: source link checker will not follow a symlink")
            continue
        prose = markdown_prose(source.read_text(encoding="utf-8"))
        for link in re.findall(r"(?<!!)\[[^\]\n]+\]\(([^)\n]+)\)", prose):
            target = link.split(maxsplit=1)[0].strip("<>")
            parsed = urlsplit(target)
            if parsed.scheme or parsed.netloc:
                continue
            destination = (
                (source.parent / unquote(parsed.path)).resolve()
                if parsed.path
                else source
            )
            if not destination.is_relative_to(repo):
                errors.append(f"{relative}: repository link escapes checkout: {target}")
            elif not destination.exists():
                errors.append(f"{relative}: missing repository target: {target}")
            elif (
                parsed.fragment
                and destination.is_file()
                and destination.suffix == ".md"
                and unquote(parsed.fragment)
                not in markdown_anchors(destination.read_text(encoding="utf-8"))
            ):
                errors.append(f"{relative}: missing repository fragment: {target}")
    return errors


def check_repository_contracts(repo: Path) -> list[str]:
    readme = repo / "README.md"
    if not readme.is_file():
        return ["README.md: missing repository entry point"]
    text = readme.read_text(encoding="utf-8")
    if "trademarks" not in markdown_anchors(text) or TRADEMARK_GUIDELINES not in text:
        return ["README.md: retain the Trademarks section and Microsoft guideline link"]
    return []


def check_planned_content(text: str, metadata: dict[str, str]) -> list[str]:
    if metadata.get("status") != "planned":
        return []
    errors = []
    if "**Status: planned.**" not in text:
        errors.append("planned page needs a visible Status: planned label")
    if re.search(
        r"^ {0,3}(?:`{3,}|~{3,})(?:bash|sh|shell|console|yaml|yml|json|powershell)\b",
        text,
        re.MULTILINE | re.IGNORECASE,
    ):
        errors.append("planned concepts must not include executable setup examples")
    return errors


def check_editorial_style(text: str, metadata: dict[str, str]) -> list[str]:
    errors = []
    title = metadata.get("title", "").casefold()
    if title in {"current architecture", "security and data handling", "guidances"}:
        errors.append("use a concise product title: Architecture, Security, or Guides")
    prose = markdown_prose(text)
    if re.search(
        r"\b(?:current (?:implementation|source)|this checkout|"
        r"we (?:are|'re) (?:refactoring|rewriting|rebuilding)|"
        r"is being (?:refactored|rewritten|rebuilt))\b",
        prose,
        re.IGNORECASE,
    ):
        errors.append("describe product behavior rather than development progress")
    return errors


def content_route(path: Path, content: Path) -> str:
    relative = path.relative_to(content).with_suffix("")
    if relative.name == "_index":
        relative = relative.parent
    return "/" + relative.as_posix().strip(".").rstrip("/")


def check_navigation_config(site: Path) -> list[str]:
    errors = []
    config = site / "data/navigation.toml"
    if not config.is_file():
        return ["missing navigation module map"]
    modules = tomllib.loads(config.read_text())["modules"]
    if [module["title"] for module in modules] != list(MODULE_TITLES):
        errors.append("navigation module titles/order must match the top navigation")
    content = site / "content/en"
    pages = {content_route(path, content) for path in (content / "docs").rglob("*.md")}
    assigned = []
    for module in modules:
        entries = navigation_pages(module)
        if module["page"] not in entries:
            errors.append(f"module landing must appear in its sidebar: {module['id']}")
        assigned.extend(entries + module.get("extra_paths", []))
    for page in sorted(set(assigned) | pages):
        count = assigned.count(page)
        if page not in pages:
            errors.append(f"missing navigation source: {page}")
        elif count != 1:
            errors.append(
                f"navigation page needs exactly one assignment: {page} ({count})"
            )
    return errors


def check_roadmap_scope(site: Path) -> list[str]:
    """Keep plans in one short roadmap rather than spreading them into guides."""
    errors = []
    content = site / "content/en"
    found = False
    for path in (content / "docs").rglob("*.md"):
        text = path.read_text(encoding="utf-8")
        route = content_route(path, content)
        metadata = frontmatter(text)
        if route == ROADMAP:
            found = True
            if (
                metadata.get("title") != "Roadmap"
                or metadata.get("status") != "planned"
            ):
                errors.append("Roadmap must be explicitly marked as planned")
            if len(markdown_prose(text).split()) > 250:
                errors.append(
                    "Roadmap must remain a short overview (at most 250 words)"
                )
        elif metadata.get("status") == "planned":
            errors.append(f"planned content belongs only on Roadmap: {route}")
        if "platform-direction" in text or path.stem == "platform-direction":
            errors.append(f"remove the detailed platform-direction content: {route}")
    if not found:
        errors.append("missing Roadmap page")
    return errors


def check_toolchain(site: Path) -> list[str]:
    """Keep the site and its setup instructions aligned with the repository."""
    errors: list[str] = []
    repo = site.parent
    paths = (
        repo / ".go-version",
        site / "go.mod",
        site / "README.md",
        site / "content/en/docs/contributing/site.md",
        repo / ".github/workflows/cetirun-docs-validation.yml",
        site / "hugo.toml",
    )
    for path in paths:
        if not path.is_file():
            errors.append(f"missing toolchain source: {path.relative_to(repo)}")
    if errors:
        return errors
    version = paths[0].read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"1\.\d+\.\d+", version):
        return [".go-version must pin an exact Go release"]
    module = re.search(
        r"^go\s+(\S+)\s*$", paths[1].read_text(encoding="utf-8"), re.MULTILINE
    )
    if not module or module[1] != version:
        errors.append(f"site/go.mod must match .go-version ({version})")
    for path in paths[2:4]:
        versions = re.findall(r"\bGo (1\.\d+\.\d+)\b", path.read_text(encoding="utf-8"))
        if not versions or any(value != version for value in versions):
            errors.append(
                f"{path.relative_to(repo)}: document Go {version} from .go-version"
            )
    if not re.search(
        r"^\s+go-version-file:\s*\.go-version\s*$",
        paths[4].read_text(encoding="utf-8"),
        re.MULTILINE,
    ):
        errors.append("documentation CI must use go-version-file: .go-version")
    config = tomllib.loads(paths[5].read_text(encoding="utf-8"))
    if config.get("module", {}).get("proxy") != "":
        errors.append("Hugo module proxy must inherit GOPROXY for the offline gate")
    return errors


def check_content(site: Path, policy_root: Path | None = None) -> list[str]:
    errors: list[str] = []
    repo = site.parent
    content = site / "content/en/docs"
    pages = list(content.rglob("*.md"))
    if not pages:
        return ["documentation sources missing"]
    for path in pages:
        text = path.read_text(encoding="utf-8")
        metadata = frontmatter(text)
        if not metadata.get("title") or not metadata.get("description"):
            errors.append(f"{path.relative_to(site)}: missing title or description")
        if not metadata.get("weight", "").isdigit():
            errors.append(
                f"{path.relative_to(site)}: missing numeric navigation weight"
            )
        if metadata.get("draft") == "true" or metadata.get("exclude_search") == "true":
            errors.append(f"{path.relative_to(site)}: canonical docs must be visible")
        errors.extend(
            f"{path.relative_to(site)}: {error}"
            for error in check_planned_content(text, metadata)
        )
        errors.extend(
            f"{path.relative_to(site)}: {error}"
            for error in check_editorial_style(text, metadata)
        )
        for target in re.findall(r'{{<\s*repo\s+"([^"]+)"(?:\s+"tree")?\s*>}}', text):
            resolved = (repo / target).resolve()
            if target in POLICY_FILES and not resolved.exists() and policy_root:
                resolved = (policy_root / target).resolve()
                if (
                    resolved.is_relative_to(policy_root.resolve())
                    and resolved.is_file()
                ):
                    continue
            if not resolved.is_relative_to(repo.resolve()) or not resolved.exists():
                errors.append(
                    f"{path.relative_to(site)}: missing repository source {target}"
                )
        if re.search(r"\bTODO\b|(?:Azure|feiskyer)/[^/\s]+/issues/\d+", text):
            errors.append(
                f"{path.relative_to(site)}: stale TODO or cross-repository issue"
            )
        if re.search(r"\b(?:aksclaw|rebrand|migrated from)\b", text, re.IGNORECASE):
            errors.append(f"{path.relative_to(site)}: stale product narrative")
        if re.search(
            r"\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b", text, re.IGNORECASE
        ):
            errors.append(
                f"{path.relative_to(site)}: use placeholders, not deployment UUIDs"
            )
        if re.search(
            r"(?:cat|head)\s+[^\n]*(?:identity-token|auth-profiles\.json)", text
        ):
            errors.append(f"{path.relative_to(site)}: credential-dumping example")
    for section in SECTIONS:
        if not (content / section / "_index.md").is_file():
            errors.append(f"missing section entry: {section}")
    for old, new in POINTERS.items():
        pointer = repo / old
        target = content / new
        if not pointer.is_file() or not target.is_file():
            errors.append(f"missing content mapping: {old} -> {new}")
        elif f"site/content/en/docs/{new}" not in pointer.read_text(encoding="utf-8"):
            errors.append(
                f"repository entry does not point to canonical content: {old}"
            )
    config = tomllib.loads((site / "hugo.toml").read_text(encoding="utf-8"))
    if config["params"].get("description") != MISSION:
        errors.append("site mission must match the supplied mission exactly")
    if MISSION not in (repo / "README.md").read_text(encoding="utf-8"):
        errors.append("root README must preserve the exact mission")
    for menu in config["menu"]["main"]:
        ref = menu.get("pageRef")
        if ref and not (site / "content/en" / ref.lstrip("/") / "_index.md").is_file():
            errors.append(f"menu page does not exist: {ref}")
    errors.extend(check_doc_inventory(site))
    errors.extend(check_repository_links(repo))
    errors.extend(check_repository_contracts(repo))
    errors.extend(check_navigation_config(site))
    errors.extend(check_roadmap_scope(site))
    return errors


def contrast(foreground: str, background: str) -> float:
    def luminance(color: str) -> float:
        channels = [int(color[i : i + 2], 16) / 255 for i in (1, 3, 5)]
        linear = [
            v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4
            for v in channels
        ]
        return sum(v * w for v, w in zip(linear, (0.2126, 0.7152, 0.0722)))

    light, dark = sorted((luminance(foreground), luminance(background)), reverse=True)
    return (light + 0.05) / (dark + 0.05)


def check_styles(path: Path) -> list[str]:
    text = path.read_text(encoding="utf-8")
    errors: list[str] = []
    for selector in (":root", 'html[data-bs-theme="dark"]'):
        match = re.search(re.escape(selector) + r"\s*\{([^}]+)\}", text)
        if not match:
            errors.append(f"missing theme: {selector}")
            continue
        tokens = dict(re.findall(r"--cr-([\w-]+):\s*(#[\da-fA-F]{6});", match[1]))
        for foreground in ("text", "muted", "link"):
            for background in ("bg", "panel", "soft"):
                if contrast(tokens[foreground], tokens[background]) < 4.5:
                    errors.append(
                        f"{selector}: insufficient {foreground}/{background} contrast"
                    )
        if contrast(tokens["on-accent"], tokens["accent"]) < 4.5:
            errors.append(f"{selector}: insufficient button contrast")
    for marker in (
        ":focus-visible",
        ".cr-skip:focus",
        "prefers-reduced-motion",
        "max-width: 767px",
    ):
        if marker not in text:
            errors.append(f"missing accessibility/responsive rule: {marker}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--public", type=Path, required=True, help="Built Hugo output directory"
    )
    parser.add_argument(
        "--base-url", required=True, help="Absolute base URL used for this build"
    )
    parser.add_argument(
        "--policy-root",
        type=Path,
        help="Optional verified policy checkout for independent documentation worktrees",
    )
    args = parser.parse_args()
    base = urlsplit(args.base_url)
    if (
        base.scheme not in {"http", "https"}
        or not base.netloc
        or not base.path.endswith("/")
    ):
        parser.error("--base-url must be an absolute HTTP(S) URL ending in /")
    root = args.public.resolve()
    if not root.is_dir():
        parser.error("--public must be an existing built site directory")
    errors = check_generated(root, args.base_url)
    errors.extend(check_content(SITE, args.policy_root))
    errors.extend(check_toolchain(SITE))
    errors.extend(check_styles(SITE / "assets/scss/_styles_project.scss"))
    for logo in (SITE.parent / "docs/logo.jpg", root / "images/logo.jpg"):
        if (
            not logo.is_file()
            or hashlib.sha256(logo.read_bytes()).hexdigest() != LOGO_SHA256
        ):
            errors.append(f"original logo bytes not preserved: {logo}")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(
        f"Checked {len(list(root.rglob('*.html')))} HTML pages: links, fragments, "
        "navigation, search, content, repository sources, toolchain, logo, "
        "and static accessibility."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
