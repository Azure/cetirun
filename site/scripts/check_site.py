#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Validate a built CetiRun site without network, browser, or cloud access."""

from __future__ import annotations

import argparse
import bisect
import hashlib
import json
import re
import sys
from collections import Counter
from collections.abc import Iterable, Iterator
from html.parser import HTMLParser
from itertools import accumulate
from pathlib import Path
from typing import NamedTuple
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
    "docs/agent-policy-admin.design.md": "reference/crds.md",
    "docs/guides/tailscale.md": "guides/tailscale.md",
    "docs/live-copilot-model-selection.design.md": "guides/models.md",
    "app/cetirun-chart/README.md": "reference/helm.md",
    "app/cetirun-operator/README.md": "reference/crds.md",
    "infra/README.md": "operations/infrastructure.md",
    "infra/storage/README.md": "operations/storage.md",
    "infra/istio/README.md": "operations/istio.md",
    "infra/cert-manager/README.md": "operations/public-ingress.md",
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


FRONTMATTER = re.compile(r"---\n(.*?)\n---\n", re.DOTALL)
HEADING = re.compile(r"#{1,6}\s+(.+?)\s*#*\s*$")
# One Hugo heading attribute, such as #custom-id, .class, or key=value.
HEADING_ATTRIBUTE = re.compile(r"[#.][\w-]+|[\w-]+=[^\s}]+")
MARKDOWN_LINK = re.compile(r"(?<!!)\[([^\]\n]+)\]\(([^)\n]+)\)")


def frontmatter_lines(text: str) -> dict[str, tuple[int, str]]:
    """Return the line number and the value of each frontmatter field."""
    match = FRONTMATTER.match(text)
    fields = {}
    for number, line in enumerate(match[1].splitlines() if match else [], 2):
        if field := re.match(r"(\w+):\s*(.+)$", line):
            fields[field[1]] = (number, field[2].strip().strip("\"'"))
    return fields


def frontmatter(text: str) -> dict[str, str]:
    return {key: value for key, (_, value) in frontmatter_lines(text).items()}


def prose_lines(text: str) -> list[str]:
    """Blank frontmatter and fenced examples, but keep the line numbers."""
    lines = text.splitlines()
    match = FRONTMATTER.match(text)
    start = match[0].count("\n") if match else 0
    prose = [""] * start
    fence = ""
    for line in lines[start:]:
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", line)
        if fence:
            if (
                marker
                and marker[1][0] == fence[0]
                and len(marker[1]) >= len(fence)
                and not marker[2].strip()
            ):
                fence = ""
            line = ""
        elif marker:
            fence = marker[1]
            line = ""
        prose.append(line)
    return prose


def markdown_prose(text: str) -> str:
    """Ignore frontmatter and fenced examples when checking source links."""
    return "\n".join(prose_lines(text))


def split_attributes(line: str) -> tuple[str, str]:
    """Split Hugo attributes, such as {#custom-id .class}, from the end of a line.

    Return the text before the attributes and the custom anchor, or "" if there
    is no anchor. A line without valid attributes stays the same. String methods
    keep the time linear: one regular expression for this grammar can backtrack
    exponentially.
    """
    body = line.rstrip()
    start = body.rfind("{")
    if start < 0 or not body.endswith("}"):
        return line, ""
    tokens = body[start + 1 : -1].split()
    if not all(HEADING_ATTRIBUTE.fullmatch(token) for token in tokens):
        return line, ""
    anchor = next((token[1:] for token in tokens if token.startswith("#")), "")
    return body[:start].rstrip(), anchor


def heading_lines(prose: list[str]) -> list[tuple[int, str, str]]:
    """Return the line, the text, and the custom anchor or "" of each heading."""
    return [
        (number, *split_attributes(match[1]))
        for number, line in enumerate(prose, 1)
        if (match := HEADING.match(line))
    ]


def markdown_headings(prose: list[str]) -> dict[str, str]:
    """Map each heading anchor to its text, with the suffixes that Hugo adds."""
    headings: dict[str, str] = {}
    seen: Counter[str] = Counter()
    for _, text, anchor in heading_lines(prose):
        if anchor:
            seen[anchor] += 1
        else:
            slug = re.sub(r"[^\w -]", "", text.lower()).replace(" ", "-")
            anchor = f"{slug}-{seen[slug]}" if seen[slug] else slug
            seen[slug] += 1
        headings[anchor] = text
    return headings


def markdown_anchors(text: str) -> set[str]:
    prose = prose_lines(text)
    anchors = set(re.findall(r'<a\s+(?:id|name)=["\']([^"\']+)', "\n".join(prose)))
    return anchors | set(markdown_headings(prose))


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
        for match in MARKDOWN_LINK.finditer(prose):
            target = match[2].split(maxsplit=1)[0].strip("<>")
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


class StyleRule(NamedTuple):
    name: str
    level: str
    advice: str
    pattern: str = ""


class StyleFinding(NamedTuple):
    rule: StyleRule
    line: int
    text: str

    def __str__(self) -> str:
        text = self.text if len(self.text) <= 90 else self.text[:87] + "..."
        return f"line {self.line}: {self.rule.advice} ({self.rule.name}): {text}"


class Block(NamedTuple):
    """A paragraph, list item, numbered step, or quote, joined into one line."""

    kind: str
    first: int
    offsets: list[int]
    text: str

    def line(self, offset: int) -> int:
        """Return the source line of a character in the joined text."""
        return self.first + bisect.bisect_right(self.offsets, offset) - 1


class Page(NamedTuple):
    """The parts of a page that the cross-page rules compare."""

    fields: dict[str, tuple[int, str]]
    prose: list[str]
    headings: dict[str, str]

    def field(self, key: str) -> str:
        return self.fields.get(key, (0, ""))[1]


STEP_WORDS, SENTENCE_WORDS, PARAGRAPH_SENTENCES, PAGE_LINES = 20, 25, 6, 400
# Style rules for documentation pages. An "error" rule fails the check. A
# "pending" rule only reports, because pages do not meet it yet: when
# `check_site.py --style-report` shows no findings for it, change its level to
# "error". A "review" rule marks text for a person to judge and only reports.
# Pattern rules ignore code, link targets, and bold UI labels.
STYLE_LEVELS = ("error", "pending", "review")
STYLE_RULES = (
    StyleRule(
        "contraction",
        "error",
        'write the full form, such as "do not"',
        r"\b(?:\w+n['’]t|(?:it|that|there|here|what|who|let)['’]s"
        r"|(?:i|you|we|they)['’](?:m|re|ve|ll|d)|(?:he|she|it|that)['’](?:ll|d))\b",
    ),
    StyleRule(
        "phrasal-verb",
        "error",
        "use one verb, such as start, stop, remove, or find",
        r"\b(?:(?:spin|spins|spinning|spun|start|starts|starting|started"
        r"|bring|brings|bringing|brought|look|looks|looking|looked)\s+up"
        r"|(?:shut|shuts|shutting|tear|tears|tearing|tore|torn)\s+down"
        r"|(?:find|finds|finding|found|carry|carries|carrying|carried"
        r"|figure|figures|figuring|figured)\s+out"
        r"|(?:fill|fills|filling|filled)\s+(?:in|out))\b",
    ),
    StyleRule(
        "phrasal-verb",
        "pending",
        "use one verb, such as configure, enable, disable, or remove",
        r"\b(?:(?:set|sets|setting|clean|cleans|cleaning|cleaned"
        r"|back|backs|backing|backed)\s+up"
        r"|(?:turn|turns|turning|turned)\s+(?:on|off)"
        r"|(?:roll|rolls|rolling|rolled)\s+back)\b",
    ),
    StyleRule("link-text", "error", "name the target in the link text"),
    StyleRule(
        "dev-narrative",
        "error",
        "describe product behavior rather than development progress",
        r"\b(?:current\s+(?:implementation|source\s+code)|this\s+checkout"
        r"|if\s+the\s+checkout\s+includes|earlier\s+checkout|this\s+increment"
        r"|separately\s+delivered|not\s+defined\s+yet|now\s+supplies"
        r"|we\s+(?:are|['’]re)\s+(?:refactoring|rewriting|rebuilding)"
        r"|(?:is|are|was|were)\s+being\s+(?:refactored|rewritten|rebuilt))\b",
    ),
    StyleRule(
        "dev-narrative",
        "pending",
        "describe product behavior rather than development progress",
        r"\b(?:this\s+repository\s+change|initial\s+scope|in\s+this\s+version"
        r"|now\s+exports|in\s+a\s+measurement|measured\s+on\s+an?\s+test)\b",
    ),
    StyleRule(
        "verb-tense",
        "error",
        'use the simple present tense, such as "the operator deletes the Agent"',
        r"\b(?:is|are|was|were)\s+being\s+\w+",
    ),
    StyleRule(
        "timeless",
        "error",
        "describe the product without reference to a date or a release",
        r"\b(?:currently|presently|nowadays|(?<!\bas\s)soon|recently|anymore"
        r"|for\s+now|at\s+(?:present|the\s+moment|this\s+time)|as\s+of"
        r"|in\s+the\s+future|going\s+forward)\b",
    ),
    StyleRule(
        "timeless",
        "review",
        "make sure that the text describes the product, not a release change",
        r"\b(?:now|no\s+longer)\b",
    ),
    StyleRule(
        "minimizing-word",
        "error",
        "remove words that tell the reader that a task is easy",
        r"\b(?:simply|easy|easily|obviously|of\s+course|basically)\b",
    ),
    StyleRule(
        "first-person",
        "pending",
        'address the reader; do not use "we"',
        r"\b(?:we|ours?|(?-i:us))\b",
    ),
    StyleRule(
        "examine-subject",
        "review",
        'use "check" when a tool or a component does the action',
        r"\b(?:it|this|that|which|commands?|checks?|scripts?|helpers?|validators?"
        r"|tests?|renders?|charts?|webhooks?|operators?|controllers?|portal"
        r"|gateway|gates?|jobs?|plans?|preflights?|probes?|tools?|workflows?"
        r"|workers?)\s+(?:(?:also|only|then|first|still|again|does\s+not"
        r"|do\s+not|cannot)\s+)?examines?\b",
    ),
    StyleRule("repo-url", "pending", "use the repo shortcode for repository paths"),
    StyleRule("heading-case", "pending", "use sentence case"),
    StyleRule(
        "description",
        "pending",
        "start with the reader's task or the purpose, not with an announcement",
    ),
    StyleRule("duplicate-title", "pending", "give each page a unique title"),
    StyleRule(
        "step-length",
        "pending",
        f"keep each sentence of a numbered step to {STEP_WORDS} words or fewer",
    ),
    StyleRule(
        "sentence-length",
        "pending",
        f"keep each descriptive sentence to {SENTENCE_WORDS} words or fewer",
    ),
    StyleRule(
        "paragraph-length",
        "pending",
        f"keep each paragraph to {PARAGRAPH_SENTENCES} sentences or fewer",
    ),
    StyleRule(
        "negative-paragraph",
        "review",
        "most sentences are negative; tell the reader what to do",
    ),
    StyleRule(
        "link-title",
        "review",
        "use the title of the target page or section as the link text",
    ),
    StyleRule("term-case", "review", "capitalize each product term in one way"),
    StyleRule(
        "page-length",
        "review",
        f"a page of more than {PAGE_LINES} lines often mixes concepts, tasks, and"
        " reference",
    ),
)
# Names that keep their capital letters in a sentence-case heading. Keep this
# list aligned with the glossary.
PROPER_NAMES = (
    "ADX-Mon", "AI Gateway", "Agent Broker", "Agent Harness", "Agent Identity",
    "Agent Policy", "Agent", "Agents", "Azure Blob", "Azure Container Registry",
    "Azure Data Explorer", "Azure Monitor", "Azure", "Bicep", "Blob", "Broker",
    "Cilium", "Connections MCP", "Container Insights", "Copilot", "Custom Web UI",
    "Custom", "Docker", "Docsy", "Entra", "Gateway", "GitHub App", "Google Cloud",
    "Google", "Helm", "Hermes", "Hugo", "Istio", "Kata", "Key Vault", "Kubernetes",
    "Let's Encrypt", "Linux", "Memory Context", "Microsoft Graph", "Microsoft",
    "Node.js", "OAuth App", "OpenClaw Control UI", "Pod", "Pods", "Portal Chat",
    "Portal", "Python", "Sandbox", "Sandboxes", "Secret", "Secrets",
    "Skill Marketplace", "Tailscale", "Team", "Teams", "User Guide",
)  # fmt: skip
GENERIC_LINK_TEXT = {
    "click here", "here", "learn more", "link", "more", "read more", "see here",
    "this", "this document", "this guide", "this link", "this page", "this section",
}  # fmt: skip
CASED_TERMS = ("Agent", "Team", "Sandbox", "Portal", "Operator", "Gateway")
ANNOUNCEMENT = re.compile(
    r"(?:this|the\s+following)\s+(?:page|guide|section|document|topic)\s+"
    r"(?:describes|explains|shows|covers|lists|contains|gives|provides"
    r"|introduces)\b|(?:an?\s+)?(?:introduction|overview)\s+(?:to|of)\b",
    re.IGNORECASE,
)
CODE_SPAN = re.compile(r"`[^`\n]+`")
# Bold UI labels and quotations. Like a link, each one can wrap once.
LABEL = re.compile(
    r'\*\*[^*\n]+(?:\n[^*\n]+)?\*\*|"[^"\n]*(?:\n[^"\n]*)?"|“[^”\n]*(?:\n[^”\n]*)?”'
)
LINK_TEXT = re.compile(r"!?\[([^\]\n]*(?:\n[^\]\n]*)?)\]\([^)\n]*\)")
LIST_MARKER = r"\s*(?:([-*+])|\d+[.)])\s+"
LIST_ITEM = re.compile(LIST_MARKER + "(.+)")
NEGATION = re.compile(r"\b(?:not|cannot|never)\b|n['’]t\b", re.IGNORECASE)
REPO_URL = re.compile(
    r"https?://github\.com/Azure/cetirun/(?:blob|tree|raw|edit)/[^\s)>\]\"']*",
    re.IGNORECASE,
)
SENTENCE_END = re.compile(r"(?<=[.!?])\s+(?=[\"'(\[]?[A-Z0-9])")
TERM = re.compile(
    r"(?<=[a-z,][ \n])("
    + "|".join(f"{term}|{term.lower()}" for term in CASED_TERMS)
    + r")(?:e?s)?\b"
)
WORD = re.compile(r"\S*\w\S*")


def names_pattern(names: Iterable[str]) -> re.Pattern[str]:
    """Match any of the names, longest first, also when a name wraps."""
    words = [
        r"\s+".join(re.escape(word).replace("'", "['’]") for word in name.split())
        for name in sorted(names, key=len, reverse=True)
    ]
    return re.compile(r"\b(?:" + ("|".join(words) or "(?!)") + r")\b")


PROPER_NAME = names_pattern(PROPER_NAMES)
# The pattern rules skip a name that one of them matches, such as Let's Encrypt.
RULE_NAME = names_pattern(
    name
    for name in PROPER_NAMES
    if any(
        re.search(rule.pattern, name, re.IGNORECASE)
        for rule in STYLE_RULES
        if rule.pattern
    )
)


def style_rule(name: str) -> StyleRule:
    """Return the rule of a check that is not a pattern; it has one level."""
    (rule,) = [rule for rule in STYLE_RULES if rule.name == name and not rule.pattern]
    return rule


def found(name: str, line: int, text: str) -> StyleFinding:
    return StyleFinding(style_rule(name), line, text)


def mask(pattern: re.Pattern[str], placeholder: str, text: str) -> str:
    """Replace each match with a placeholder, but keep its line breaks."""
    return pattern.sub(lambda match: placeholder + "\n" * match[0].count("\n"), text)


def plain_text(text: str) -> str:
    """Keep the words and the line breaks of Markdown text.

    Code and URLs become placeholders, and links keep only their text.
    Shortcodes, HTML tags, and heading attributes are removed.
    """
    text = CODE_SPAN.sub("CODE", text)
    text = re.sub(r"\{\{[<%].*?[%>]\}\}", "", text)
    text = LINK_TEXT.sub(r"\1", text)
    text = re.sub(r"<[^>\n]*>", "", text)
    text = "\n".join(split_attributes(line)[0] for line in text.split("\n"))
    return re.sub(r"https?://\S+", "URL", text)


def offsets(parts: list[str]) -> list[int]:
    """Return where each part starts when the parts are joined with spaces."""
    return list(accumulate((len(part) + 1 for part in parts[:-1]), initial=0))


def text_blocks(lines: list[str]) -> list[Block]:
    """Group prose lines into paragraphs, list items, numbered steps, and quotes."""
    blocks: list[tuple[str, int, list[str]]] = []
    current = None
    for number, line in enumerate(lines, 1):
        stripped = line.strip()
        item = LIST_ITEM.match(line)
        if not stripped or stripped.startswith(("#", "|", "<")):
            current = None
            continue
        if item:
            kind, body = ("item" if item[1] else "step"), item[2]
        elif stripped.startswith(">"):
            kind, body = "quote", stripped.lstrip("> ")
            if not body:
                current = None
                continue
        else:
            kind, body = "paragraph", stripped
        if item or not current or (kind == "quote") != (current[0] == "quote"):
            current = (kind, number, [])
            blocks.append(current)
        current[2].append(body)
    return [
        Block(kind, first, offsets(parts), " ".join(parts))
        for kind, first, parts in blocks
    ]


def sentence_spans(text: str) -> list[tuple[int, str]]:
    """Split text into sentences; abbreviations and bold markers do not end them."""
    masked = re.sub(
        r"\b(?:e\.g|i\.e|etc|vs)\.",
        lambda match: match[0].replace(".", "_"),
        text.replace("**", "  "),
        flags=re.IGNORECASE,
    )
    spans, start = [], 0
    for end in [*SENTENCE_END.finditer(masked), None]:
        stop = end.start() if end else len(text)
        if sentence := text[start:stop].replace("**", "").strip():
            spans.append((start, sentence))
        start = end.end() if end else stop
    return spans


def sentence_case_words(heading: str) -> list[str]:
    """Return the words of a heading that sentence case writes in lowercase."""
    text = mask(PROPER_NAME, "NAME", mask(LABEL, "UI", plain_text(heading)))
    words = []
    for clause in re.split(r":\s+", text):
        for token in re.findall(r"[A-Za-z][\w'-]*", clause)[1:]:
            # A hyphenated word with a digit, such as Decision-1, is a name.
            if not re.search(r"\d", token):
                words.extend(
                    part
                    for part in token.split("-")
                    if part[:1].isupper() and part[1:].islower()
                )
    return words


def word_findings(lines: list[str]) -> list[StyleFinding]:
    """Apply the pattern rules and compare the capitals of product terms.

    A match of a stricter rule, or of an earlier rule of the same level, hides
    an overlapping match of another rule. Bold UI labels, quotations, and names
    that a rule matches are not checked.
    """
    text = mask(RULE_NAME, "NAME", mask(LABEL, "UI", "\n".join(lines)))

    def line(offset: int) -> int:
        return text.count("\n", 0, offset) + 1

    findings: list[StyleFinding] = []
    taken: list[tuple[int, int]] = []
    for rule in sorted(
        (rule for rule in STYLE_RULES if rule.pattern),
        key=lambda rule: STYLE_LEVELS.index(rule.level),
    ):
        for match in re.finditer(rule.pattern, text, re.IGNORECASE):
            if any(start < match.end() and match.start() < end for start, end in taken):
                continue
            taken.append(match.span())
            excerpt = " ".join(match[0].split())
            findings.append(StyleFinding(rule, line(match.start()), excerpt))
    forms: dict[str, dict[str, list[int]]] = {term: {} for term in CASED_TERMS}
    for match in TERM.finditer(text):
        forms[match[1].capitalize()].setdefault(match[1], []).append(match.start())
    for term, spellings in forms.items():
        if len(spellings) == 2:
            lower = term.lower()
            rare = min(spellings, key=lambda form: len(spellings[form]))
            counts = f"{term}: {len(spellings[term])}, {lower}: {len(spellings[lower])}"
            findings.append(found("term-case", line(spellings[rare][0]), counts))
    return findings


def link_findings(prose: list[str]) -> list[StyleFinding]:
    findings = []
    for number, line in enumerate(prose, 1):
        line = CODE_SPAN.sub("CODE", line)
        for match in MARKDOWN_LINK.finditer(line):
            label = match[1]
            if " ".join(label.strip(" .:*_").casefold().split()) in GENERIC_LINK_TEXT:
                findings.append(found("link-text", number, label))
        findings += [found("repo-url", number, url) for url in REPO_URL.findall(line)]
    return findings


def heading_findings(
    fields: dict[str, tuple[int, str]], prose: list[str]
) -> list[StyleFinding]:
    headings = [fields[key] for key in ("title", "linkTitle") if key in fields]
    headings += [(number, text) for number, text, _ in heading_lines(prose)]
    return [
        found("heading-case", number, f"{heading} ({', '.join(words)})")
        for number, heading in headings
        if (words := sentence_case_words(heading))
    ]


def opening_findings(
    fields: dict[str, tuple[int, str]], blocks: list[Block]
) -> list[StyleFinding]:
    """Check the description and the first paragraph for an announcement."""
    openings = [fields["description"]] if "description" in fields else []
    if first := next((block for block in blocks if block.kind == "paragraph"), None):
        openings.append((first.first, first.text))
    return [
        found("description", number, text)
        for number, text in openings
        if ANNOUNCEMENT.match(text)
    ]


def block_findings(blocks: list[Block]) -> list[StyleFinding]:
    """Check the length of sentences and paragraphs, and their negations."""
    findings = []
    for block in blocks:
        spans = sentence_spans(block.text)
        name, limit = "sentence-length", SENTENCE_WORDS
        if block.kind == "step":
            name, limit = "step-length", STEP_WORDS
        for offset, sentence in spans:
            if (count := len(WORD.findall(sentence))) > limit:
                text = f"{count} words: {sentence}"
                findings.append(found(name, block.line(offset), text))
        if len(spans) > PARAGRAPH_SENTENCES:
            text = f"{len(spans)} sentences"
            findings.append(found("paragraph-length", block.first, text))
        negative = sum(1 for _, sentence in spans if NEGATION.search(sentence))
        if len(spans) >= 3 and negative * 2 > len(spans):
            text = f"{negative} of {len(spans)} sentences: {block.text}"
            findings.append(found("negative-paragraph", block.first, text))
    return findings


def page_style_findings(text: str) -> list[StyleFinding]:
    """Apply the page rules at every level; the caller decides what fails."""
    prose = prose_lines(text)
    fields = frontmatter_lines(text)
    plain = plain_text("\n".join(prose)).split("\n")
    words = list(plain)
    for key in ("title", "linkTitle", "description"):
        if key in fields:
            number, value = fields[key]
            words[number - 1] = plain_text(value)
    blocks = text_blocks(plain)
    findings = [
        *word_findings(words),
        *link_findings(prose),
        *heading_findings(fields, prose),
        *opening_findings(fields, blocks),
        *block_findings(blocks),
    ]
    if (count := len(text.splitlines())) > PAGE_LINES:
        findings.append(found("page-length", 1, f"{count} lines"))
    return sorted(findings, key=lambda finding: finding.line)


def check_editorial_style(text: str, metadata: dict[str, str]) -> list[str]:
    errors = []
    title = metadata.get("title", "").casefold()
    if title in {"current architecture", "security and data handling", "guidances"}:
        errors.append("use a concise product title: Architecture, Security, or Guides")
    errors.extend(
        str(finding)
        for finding in page_style_findings(text)
        if finding.rule.level == "error"
    )
    return errors


def link_name(text: str) -> str:
    return " ".join(re.sub(r"[`*_]", "", text).casefold().split())


def duplicate_title_findings(
    pages: dict[str, Page],
) -> list[tuple[str, StyleFinding]]:
    routes_by_title: dict[str, list[str]] = {}
    for route, page in pages.items():
        if title := page.field("title"):
            routes_by_title.setdefault(title.casefold(), []).append(route)
    findings = []
    for routes in routes_by_title.values():
        if len(routes) < 2:
            continue
        for route in routes:
            number, title = pages[route].fields["title"]
            others = ", ".join(other for other in routes if other != route)
            text = f"{title} (also {others})"
            findings.append((route, found("duplicate-title", number, text)))
    return findings


def naming_links(prose: list[str]) -> Iterator[tuple[int, str, str]]:
    """Yield the line, the text, and the target of each link that names its target.

    A link names its target after "Refer to" or "See", or at the start of a
    list item.
    """
    for number, line in enumerate(prose, 1):
        for match in MARKDOWN_LINK.finditer(line):
            before = line[: match.start()]
            if re.search(
                r"\b(?:refer\s+to|see)\s+(?:the\s+)?$", before, re.I
            ) or re.fullmatch(LIST_MARKER, before):
                yield number, match[1], match[2].split(maxsplit=1)[0].strip("<>")


def link_title_findings(pages: dict[str, Page]) -> list[tuple[str, StyleFinding]]:
    """Compare the text of a link that names its target with the target title."""
    findings = []
    for route, page in pages.items():
        for number, label, target in naming_links(page.prose):
            if target.startswith("{{"):
                continue
            parts = urlsplit(urljoin(route + "/", target))
            destination = None if parts.scheme else pages.get(parts.path.rstrip("/"))
            if not destination:
                continue
            names = {link_name(destination.field(k)) for k in ("title", "linkTitle")}
            expected = destination.field("title")
            if parts.fragment:
                expected = destination.headings.get(parts.fragment)
                names.add(link_name(expected or ""))
            if link_name(label) not in names - {""}:
                text = f"{label} (target: {expected})"
                findings.append((route, found("link-title", number, text)))
    return findings


def navigation_title_findings(
    navigation: Path, pages: dict[str, Page]
) -> list[StyleFinding]:
    """Check the case of navigation groups and match it with their page titles."""
    source = navigation.read_text(encoding="utf-8")
    lines = source.splitlines()
    titles = {route: page.field("title") for route, page in pages.items()}
    findings = []
    for module in tomllib.loads(source)["modules"]:
        for group in module["groups"]:
            if not (title := group["title"]):
                continue
            problems = sentence_case_words(title) + [
                f"page title {titles[page]}"
                for page in group["pages"]
                if titles.get(page, "").casefold() == title.casefold()
                and titles[page] != title
            ]
            if problems:
                number = next(
                    (n for n, line in enumerate(lines, 1) if f'"{title}"' in line), 1
                )
                text = f"{title} ({', '.join(problems)})"
                findings.append(found("heading-case", number, text))
    return findings


def cross_page_style_findings(site: Path) -> dict[str, list[StyleFinding]]:
    """Apply the rules that compare pages with each other or with navigation."""
    content = site / "content/en"
    paths = {
        content_route(path, content): path
        for path in sorted((content / "docs").rglob("*.md"))
    }
    pages = {}
    for route, path in paths.items():
        text = path.read_text(encoding="utf-8")
        prose = prose_lines(text)
        pages[route] = Page(frontmatter_lines(text), prose, markdown_headings(prose))
    findings: dict[str, list[StyleFinding]] = {}
    for route, finding in [
        *duplicate_title_findings(pages),
        *link_title_findings(pages),
    ]:
        findings.setdefault(paths[route].relative_to(site).as_posix(), []).append(
            finding
        )
    if navigation := navigation_title_findings(site / "data/navigation.toml", pages):
        findings["data/navigation.toml"] = navigation
    return findings


def style_report(site: Path) -> dict[str, list[StyleFinding]]:
    """Return every style finding of the site at every level, by file."""
    report = {}
    for path in sorted((site / "content/en/docs").rglob("*.md")):
        if findings := page_style_findings(path.read_text(encoding="utf-8")):
            report[path.relative_to(site).as_posix()] = findings
    for page, findings in cross_page_style_findings(site).items():
        report[page] = sorted(
            report.get(page, []) + findings, key=lambda finding: finding.line
        )
    return dict(sorted(report.items()))


def format_style_report(report: dict[str, list[StyleFinding]]) -> str:
    lines = []
    for page, findings in report.items():
        lines.append(page)
        lines.extend(f"  {finding.rule.level}: {finding}" for finding in findings)
    counts = Counter(finding.rule for items in report.values() for finding in items)
    files = Counter(
        rule for items in report.values() for rule in {item.rule for item in items}
    )
    lines += ["", f"{'Rule':20} {'Level':8} {'Findings':>8} {'Files':>6}"]
    lines += [
        f"{rule.name:20} {rule.level:8} {counts[rule]:8} {files[rule]:6}"
        for rule in STYLE_RULES
    ]
    ready = [
        rule.name
        for rule in STYLE_RULES
        if rule.level == "pending" and not counts[rule]
    ]
    lines.append(
        "Pending rules without findings (change them to error): "
        + (", ".join(ready) or "none")
    )
    return "\n".join(lines)


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
    errors.extend(
        f"{page}: {finding}"
        for page, findings in cross_page_style_findings(site).items()
        for finding in findings
        if finding.rule.level == "error"
    )
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
    parser.add_argument("--public", type=Path, help="Built Hugo output directory")
    parser.add_argument("--base-url", help="Absolute base URL used for this build")
    parser.add_argument(
        "--policy-root",
        type=Path,
        help="Optional verified policy checkout for independent documentation worktrees",
    )
    parser.add_argument(
        "--style-report",
        action="store_true",
        help="Print the style findings of every level from the sources and exit",
    )
    args = parser.parse_args()
    if args.style_report:
        print(format_style_report(style_report(SITE)))
        return 0
    if args.public is None or args.base_url is None:
        parser.error("--public and --base-url are required")
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
    levels = Counter(
        finding.rule.level
        for findings in style_report(SITE).values()
        for finding in findings
    )
    print(
        f"Style rules that only report: {levels['pending']} pending and "
        f"{levels['review']} review findings. For the list, run "
        "scripts/check_site.py --style-report."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
