#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Generate the field and value tables of the Reference pages.

The custom resource definitions and the chart ``values.yaml`` files are the
sources. Each table sits between two HTML comments in a Markdown page:

    <!-- generated: ID -->
    ...
    <!-- /generated: ID -->

The script owns every column except Description. A Description cell that a
writer edits stays as it is. A new row starts with the description of its
source: the CRD field description, or the ``# --`` comment of a chart value,
as helm-docs reads it.

Run ``python3 site/scripts/reference_tables.py --write`` after a change to a
CRD or a ``values.yaml`` file. Without ``--write``, the script only reports the
tables that are out of date. It also reports a value path in the curated tables
of the Helm configuration page that no chart declares. The site tests run the
same checks.

The script uses only the Python standard library, so the documentation checks
need no packages. Its YAML loader accepts the subset of YAML that these sources
use and stops with an error on any other construct.
"""

import argparse
import functools
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

SITE = Path(__file__).resolve().parents[1]
REPO = SITE.parent
CONTENT = SITE / "content/en/docs"
COMMAND = "python3 site/scripts/reference_tables.py --write"


# --------------------------------------------------------------------------
# YAML subset loader
# --------------------------------------------------------------------------


class YamlError(ValueError):
    """The text uses YAML that this loader does not accept."""


@dataclass
class Node:
    """A parsed value and the comment lines directly above its key or entry."""

    value: object
    comment: str = ""


_NULLS = {"", "~", "null", "Null", "NULL"}
_TRUE = {"yes", "Yes", "YES", "true", "True", "TRUE", "on", "On", "ON"}
_FALSE = {"no", "No", "NO", "false", "False", "FALSE", "off", "Off", "OFF"}
_INT = re.compile(
    r"[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?(?:0|[1-9][0-9_]*)"
    r"|[-+]?0x[0-9a-fA-F_]+|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+"
)
_FLOAT = re.compile(
    r"[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?"
    r"|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?"
    r"|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*"
    r"|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN)"
)
_TIMESTAMP = re.compile(r"[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[Tt ].*)?")
_ESCAPES = {
    "0": "\0", "a": "\a", "b": "\b", "t": "\t", "\t": "\t", "n": "\n",
    "v": "\v", "f": "\f", "r": "\r", "e": "\x1b", " ": " ", '"': '"',
    "/": "/", "\\": "\\", "N": "\x85", "_": "\xa0", "L": "\u2028",
    "P": "\u2029",
}
_QUOTED = {
    '"': re.compile(r'"(?:[^"\\]|\\.)*"', re.DOTALL),
    "'": re.compile(r"'(?:[^']|'')*'(?!')", re.DOTALL),
}
_KEY_END = re.compile(r":(?:\s|$)")
_COMMENT = re.compile(r"(?:^|\s)#")
_KEY_COMMENT = re.compile(r"\s#")
_TAB_INDENT = re.compile(r" *\t")
_INDICATORS = "[]{},#&*!|>%@`"


def resolve_plain(text: str) -> object:
    """Resolve a plain scalar with the YAML 1.1 rules that PyYAML uses."""
    if text in _NULLS:
        return None
    if text in _TRUE:
        return True
    if text in _FALSE:
        return False
    if text in {"<<", "="}:
        raise YamlError(f"unsupported YAML scalar: {text}")
    if _INT.fullmatch(text):
        return _number(text.replace("_", ""), integer=True)
    if _FLOAT.fullmatch(text):
        return _number(text.replace("_", "").lower(), integer=False)
    if _TIMESTAMP.fullmatch(text):
        raise YamlError(f"quote this timestamp-like scalar: {text}")
    return text


def _number(text: str, integer: bool) -> int | float:
    sign = -1 if text.startswith("-") else 1
    body = text.lstrip("+-")
    if ":" in body:
        total = 0
        for part in body.split(":"):
            total = total * 60 + (int(part) if integer else float(part))
        return sign * total
    if not integer:
        if body == ".inf":
            return sign * float("inf")
        if body == ".nan":
            return float("nan")
        return sign * float(body)
    if body.startswith("0b"):
        return sign * int(body[2:], 2)
    if body.startswith("0x"):
        return sign * int(body[2:], 16)
    if len(body) > 1 and body.startswith("0"):
        return sign * int(body, 8)
    return sign * int(body)


def _indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def _is_noise(line: str) -> bool:
    stripped = line.strip()
    return not stripped or stripped.startswith("#")


def _is_entry(content: str) -> bool:
    return content == "-" or content.startswith("- ")


def _strip_comment(text: str) -> str:
    """Remove a trailing comment from text that contains no quoted scalar."""
    match = _COMMENT.search(text)
    return text[: match.start()] if match else text


def _fold(lines: list[str]) -> str:
    """Join the lines of a multi-line flow scalar with YAML line folding."""
    result = lines[0]
    empty = 0
    for line in lines[1:]:
        if not line:
            empty += 1
            continue
        result += "\n" * empty if empty else " "
        result += line
        empty = 0
    return result


def _trim_lines(lines: list[str]) -> list[str]:
    """Trim the white space that line folding removes from a flow scalar."""
    if len(lines) == 1:
        return lines
    return (
        [lines[0].rstrip(" \t")]
        + [line.strip(" \t") for line in lines[1:-1]]
        + [lines[-1].lstrip(" \t")]
    )


def _decode(text: str) -> str:
    result = []
    position = 0
    while position < len(text):
        char = text[position]
        if char != "\\":
            result.append(char)
            position += 1
            continue
        code = text[position + 1: position + 2]
        if code in _ESCAPES:
            result.append(_ESCAPES[code])
            position += 2
            continue
        width = {"x": 2, "u": 4, "U": 8}.get(code)
        digits = text[position + 2: position + 2 + (width or 0)]
        if not width or len(digits) != width:
            raise YamlError(f"invalid escape in a quoted scalar: \\{code}")
        result.append(chr(int(digits, 16)))
        position += 2 + width
    return "".join(result)


def _unquote(token: str) -> str:
    """Return the value of a quoted scalar token, quotes included."""
    lines = token[1:-1].split("\n")
    if token[0] == "'":
        return _fold(_trim_lines(lines)).replace("''", "'")
    lines = _trim_lines(lines)
    return _fold([_decode(line) for line in lines])


class _Parser:
    def __init__(self, text: str) -> None:
        self.lines = text.split("\n")
        self.index = 0
        first = self.next_content()
        for number, line in enumerate(self.lines, start=1):
            if _TAB_INDENT.match(line) and line.strip():
                raise YamlError(f"line {number}: tabs are not supported in indentation")
            if line.rstrip() in {"---", "..."} and number - 1 != first:
                raise YamlError(f"line {number}: only one YAML document is supported")

    def error(self, message: str) -> YamlError:
        return YamlError(f"line {self.index + 1}: {message}")

    def at_end(self) -> bool:
        return self.index >= len(self.lines)

    def skip_noise(self) -> list[str]:
        """Skip blank and comment lines; return the comments above the next line.

        A blank line ends a comment block, so only the comment lines directly
        above a key belong to it.
        """
        comments: list[str] = []
        while not self.at_end() and _is_noise(self.lines[self.index]):
            stripped = self.lines[self.index].strip()
            comments = [*comments, stripped] if stripped else []
            self.index += 1
        return comments

    def next_content(self) -> int | None:
        position = self.index
        while position < len(self.lines) and _is_noise(self.lines[position]):
            position += 1
        return position if position < len(self.lines) else None

    def document(self) -> Node:
        start = self.next_content()
        if start is not None and self.lines[start].rstrip() == "---":
            self.index = start + 1
            start = self.next_content()
        if start is None:
            return Node(None)
        node = self.block(_indent(self.lines[start]), -1)
        self.skip_noise()
        if not self.at_end():
            raise self.error("unexpected content")
        return node

    def block(self, indent: int, parent: int) -> Node:
        """Parse the node that starts at the next content line.

        The comment lines above a mapping key stay unread, so that the
        mapping can keep them with its first key.
        """
        start = self.next_content()
        content = self.lines[start][indent:]
        if _is_entry(content):
            return self.sequence(indent)
        if self.split_key(content) is not None:
            return self.mapping(indent)
        self.index = start + 1
        return self.inline(content, parent)

    def split_key(self, content: str) -> tuple[str, str] | None:
        """Return the key and the remaining text of a ``key: value`` line."""
        if not content or content[0] in _INDICATORS or _is_entry(content):
            return None
        if content[0] in _QUOTED:
            match = _QUOTED[content[0]].match(content)
            if not match or not _KEY_END.match(content, match.end()):
                return None
            return _unquote(match.group(0)), content[match.end() + 1:]
        match = _KEY_END.search(content)
        if not match:
            return None
        key = content[: match.start()].rstrip()
        if not key or _KEY_COMMENT.search(key):
            return None
        return key, content[match.end():]

    def peek_line(self, indent: int) -> str | None:
        start = self.next_content()
        if start is None or _indent(self.lines[start]) < indent:
            return None
        if _indent(self.lines[start]) > indent:
            self.index = start
            raise self.error("unexpected indentation")
        return self.lines[start][indent:]

    def mapping(self, indent: int) -> Node:
        result: dict[str, Node] = {}
        while (content := self.peek_line(indent)) is not None:
            comments = self.skip_noise()
            pair = self.split_key(content)
            if pair is None:
                if _is_entry(content):
                    raise self.error("a sequence entry cannot follow a mapping key here")
                raise self.error("expected a mapping key")
            key, rest = pair
            if key in result:
                raise self.error(f"duplicate key: {key}")
            self.index += 1
            node = self.value(rest, indent, in_mapping=True)
            node.comment = "\n".join(comments)
            result[key] = node
        return Node(result)

    def sequence(self, indent: int) -> Node:
        items: list[Node] = []
        while (content := self.peek_line(indent)) is not None and _is_entry(content):
            comments = self.skip_noise()
            body = content[1:].lstrip(" ")
            if self.split_key(body) is not None or _is_entry(body):
                column = indent + len(content) - len(body)
                self.lines[self.index] = " " * column + body
                node = self.block(column, indent)
            else:
                self.index += 1
                node = self.value(body, indent, in_mapping=False)
            node.comment = "\n".join(comments)
            items.append(node)
        return Node(items)

    def value(self, rest: str, indent: int, in_mapping: bool) -> Node:
        """Parse the value after ``key:`` or ``-`` at the given indentation."""
        text = rest.strip()
        if text and not text.startswith("#"):
            return self.inline(text, indent)
        following = self.next_content()
        if following is None:
            return Node(None)
        current = _indent(self.lines[following])
        if current > indent:
            return self.block(current, indent)
        if current == indent and in_mapping and _is_entry(self.lines[following][current:]):
            return self.sequence(indent)
        return Node(None)

    def inline(self, text: str, parent: int) -> Node:
        """Parse a scalar or flow collection that starts on the current line."""
        if text[0] in "&*!":
            raise self.error("anchors, aliases, and tags are not supported")
        if text[0] in "|>":
            return Node(self.block_scalar(text, parent))
        if text[0] in "[{":
            return Node(self.flow(text, parent))
        if text[0] in _QUOTED:
            return Node(self.quoted(text, parent))
        return Node(self.plain(text, parent))

    def indented_lines(self, parent: int, stop_at_comment: bool) -> tuple[list[str], int]:
        lines: list[str] = []
        while not self.at_end():
            line = self.lines[self.index]
            stripped = line.strip()
            if stripped and (_indent(line) <= parent or (stop_at_comment and stripped.startswith("#"))):
                break
            lines.append(line)
            self.index += 1
        trailing = 0
        while lines and not lines[-1].strip():
            lines.pop()
            self.index -= 1
            trailing += 1
        return lines, trailing

    def continuation(self, parent: int) -> list[str]:
        return [line.strip() for line in self.indented_lines(parent, True)[0]]

    def plain(self, text: str, parent: int) -> object:
        first = _strip_comment(text).strip()
        if first != text.strip():
            return resolve_plain(first)
        rest = self.continuation(parent)
        for line in rest:
            if _KEY_END.search(line) or _strip_comment(line) != line:
                raise self.error("a multi-line plain scalar cannot hold ': ' or ' #'")
        return _fold([first, *rest]) if rest else resolve_plain(first)

    def quoted(self, text: str, parent: int) -> str:
        pattern = _QUOTED[text[0]]
        buffer = text
        while not (match := pattern.match(buffer)):
            if self.at_end():
                raise self.error("unterminated quoted scalar")
            line = self.lines[self.index]
            if line.strip() and _indent(line) <= parent:
                raise self.error("a quoted scalar continues at a lower indentation")
            buffer += "\n" + line
            self.index += 1
        tail = buffer[match.end():].strip()
        if tail and not tail.startswith("#"):
            raise self.error("unexpected text after a quoted scalar")
        return _unquote(match.group(0))

    def block_scalar(self, header: str, parent: int) -> str:
        match = re.fullmatch(r"\|([-+]?)\s*(?:#.*)?", header)
        if not match:
            raise self.error(f"unsupported block scalar header: {header}")
        chomp = match.group(1)
        raw, trailing = self.indented_lines(parent, False)
        if not raw:
            return ""
        first = next(line for line in raw if line.strip())
        content = _indent(first)
        if any(line.strip() and _indent(line) < content for line in raw):
            raise self.error("block scalar line is less indented than its first line")
        body = [line[content:] if line.strip() else "" for line in raw]
        text = "\n".join(body)
        if chomp == "-":
            return text
        if chomp == "+":
            return text + "\n" * (1 + trailing)
        return text + "\n"

    def flow(self, text: str, parent: int) -> object:
        parser = _Flow(text)
        value = parser.node()
        tail = text[parser.position:].strip()
        if tail and not tail.startswith("#"):
            raise self.error("unexpected text after a flow collection")
        return value


class _Flow:
    """Parse one flow collection, for example ``[a, "b"]`` or ``{k: v}``."""

    def __init__(self, text: str) -> None:
        self.text = text
        self.position = 0

    def error(self, message: str) -> YamlError:
        return YamlError(f"flow collection {self.text!r}: {message}")

    def peek(self) -> str:
        while self.text[self.position: self.position + 1] == " ":
            self.position += 1
        if self.position >= len(self.text):
            raise self.error("unterminated flow collection")
        return self.text[self.position]

    def node(self) -> object:
        char = self.peek()
        if char == "[":
            return self.collection("]", self.sequence_entry, [])
        if char == "{":
            return self.collection("}", self.mapping_entry, {})
        if char in _QUOTED:
            match = _QUOTED[char].match(self.text, self.position)
            if not match:
                raise self.error("unterminated quoted scalar")
            self.position = match.end()
            return _unquote(match.group(0))
        if char in ("&", "*", "!"):
            raise self.error("anchors, aliases, and tags are not supported")
        start = self.position
        while self.position < len(self.text):
            char = self.text[self.position]
            following = self.text[self.position + 1: self.position + 2]
            if char in ",]}" or (char == ":" and following in (" ", ",", "]", "}", "")):
                break
            self.position += 1
        token = self.text[start: self.position].strip()
        if not token:
            raise self.error("empty flow entry")
        return resolve_plain(token)

    def collection(self, closing: str, entry, result):
        self.position += 1
        while self.peek() != closing:
            entry(result)
            char = self.peek()
            if char == ",":
                self.position += 1
            elif char != closing:
                raise self.error(f"expected , or {closing}")
        self.position += 1
        return result

    def sequence_entry(self, result: list) -> None:
        result.append(self.node())

    def mapping_entry(self, result: dict) -> None:
        key = self.node()
        if self.peek() != ":":
            raise self.error("expected : after a flow mapping key")
        self.position += 1
        if key in result:
            raise self.error(f"duplicate key: {key}")
        result[key] = self.node()


def load_nodes(text: str) -> Node:
    """Parse YAML text into nodes that keep the comment above each key."""
    return _Parser(text).document()


def plain(node: object) -> object:
    """Convert nodes to ordinary Python values."""
    if isinstance(node, Node):
        node = node.value
    if isinstance(node, dict):
        return {key: plain(value) for key, value in node.items()}
    if isinstance(node, list):
        return [plain(value) for value in node]
    return node


def load(text: str) -> object:
    return plain(load_nodes(text))


# --------------------------------------------------------------------------
# Tables
# --------------------------------------------------------------------------

QUANTITY = (
    r"^(\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|"
    r"([eE](\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))))?$"
)
CONDITION = {"lastTransitionTime", "message", "reason", "status", "type"}
REFERENCE = {"name", "uid", "generation", "namespace", "resourceVersion"}
BLOCK = re.compile(
    r"<!-- generated: (?P<id>[a-z0-9-]+) -->\n(?P<body>.*?)<!-- /generated: (?P=id) -->",
    re.DOTALL,
)
MARKER = re.compile(r"<!-- /?generated: ([^ ]+) -->")
CRD_BASES = "app/cetirun-operator/config/crd/bases"


@dataclass
class Row:
    """One table row: the key, the generated cells, and the source description."""

    key: str
    cells: list[str]
    description: str = ""


@dataclass
class Table:
    """A generated table, its source file, and the page block that holds it."""

    id: str
    page: str
    columns: tuple[str, ...]
    source: str
    build: Callable[[], list[Row]]


def code(text: str) -> str:
    """Return text as a Markdown code span that is safe in a table cell."""
    text = text.replace("|", "\\|")
    return f"`` {text} ``" if "`" in text else f"`{text}`"


def prose(text: str) -> str:
    """Return source text as one Markdown table cell."""
    text = " ".join(text.split())
    text = text.replace("|", "\\|")
    return re.sub(r"<(?=[A-Za-z/!?])", r"\\<", text)


def literal(value: object) -> str:
    return json.dumps(value, ensure_ascii=False)


# CRD field tables ---------------------------------------------------------


@functools.cache
def source_nodes(relative: str) -> Node:
    """Parse a repository YAML file once; the callers only read the nodes."""
    return load_nodes((REPO / relative).read_text(encoding="utf-8"))


def crd_path(plural: str) -> str:
    return f"{CRD_BASES}/cetirun.microsoft.com_{plural}.yaml"


@functools.cache
def crd_schema(relative: str) -> dict:
    document = plain(source_nodes(relative))
    versions = [version for version in document["spec"]["versions"] if version.get("storage")]
    if len(versions) != 1:
        raise ValueError(f"{relative}: expected one storage version")
    return versions[0]["schema"]["openAPIV3Schema"]


def crd_rows(relative: str, root: str, collapse: tuple[str, ...] = ()) -> list[Row]:
    """Return one row for each field below ``spec`` or ``status``.

    Conditions, object references, and the paths in ``collapse`` get one row
    without rows for their fields. A first row with the name of the root
    shows the rules that apply to the whole object.
    """
    rows: list[Row] = []
    schema = crd_schema(relative)["properties"][root]
    rules = _constraints(schema)
    if rules:
        rows.append(Row(root, [_type(schema), "", " ".join(rules)]))
    _property_rows(schema, "", set(collapse), rows)
    return rows


def _is_condition(schema: object) -> bool:
    return isinstance(schema, dict) and CONDITION <= set(schema.get("required", []))


def _is_reference(schema: object) -> bool:
    properties = schema.get("properties") if isinstance(schema, dict) else None
    return bool(properties) and set(properties) <= REFERENCE and all(
        value.get("type") in {"string", "integer"} for value in properties.values()
    )


def _property_rows(schema: dict, prefix: str, collapse: set[str], rows: list[Row]) -> None:
    required = set(schema.get("required", []))
    for name, child in (schema.get("properties") or {}).items():
        path = f"{prefix}.{name}" if prefix else name
        cells = [_type(child), _default(child), _validation(child, name in required)]
        rows.append(Row(path, cells, child.get("description", "")))
        if path in collapse:
            continue
        for nested, suffix in ((child, ""), (child.get("items"), "[]"), (child.get("additionalProperties"), ".<name>")):
            if isinstance(nested, dict) and nested.get("properties") and not _is_condition(nested) and not _is_reference(nested):
                _property_rows(nested, path + suffix, collapse, rows)


def _type(schema: dict) -> str:
    if schema.get("x-kubernetes-int-or-string"):
        return "quantity" if schema.get("pattern") == QUANTITY else "integer or string"
    kind = schema.get("type", "")
    if kind == "array":
        items = schema.get("items") or {}
        return "[]Condition" if _is_condition(items) else f"[]{_type(items)}"
    if kind == "object":
        values = schema.get("additionalProperties")
        if isinstance(values, dict) and values:
            return f"map[string]{_type(values)}"
        if _is_reference(schema):
            return "reference (" + ", ".join(schema["properties"]) + ")"
        return "object"
    if kind == "string" and schema.get("format") == "date-time":
        return "timestamp"
    return kind or "any"


def _default(schema: dict) -> str:
    return code(literal(schema["default"])) if "default" in schema else ""


def _validation(schema: dict, required: bool) -> str:
    parts = ["Required."] if required else []
    parts += _constraints(schema)
    if _is_reference(schema):
        fields = set(schema.get("required", []))
        for name, child in schema["properties"].items():
            details = (["Required."] if name in fields else []) + _constraints(child)
            if details:
                parts.append(f"{code(name)}: {' '.join(details)}")
    for key, label in (("items", "Each item"), ("additionalProperties", "Each value")):
        nested = schema.get(key)
        if isinstance(nested, dict) and not _is_condition(nested):
            details = _constraints(nested)
            if details:
                parts.append(f"{label}: {' '.join(details)}")
    return " ".join(parts)


def _range(schema: dict, low: str, high: str, unit: str) -> str:
    minimum, maximum = schema.get(low), schema.get(high)
    last = maximum if maximum is not None else minimum
    unit = unit[:-1] if last == 1 else unit
    if minimum is None and maximum is None:
        return ""
    if minimum is not None and maximum is not None:
        if minimum == maximum:
            return f"Exactly {minimum} {unit}."
        return f"{minimum} to {maximum} {unit}."
    if minimum is not None:
        return f"At least {minimum} {unit}."
    return f"At most {maximum} {unit}."


def _constraints(schema: dict) -> list[str]:
    parts = []
    if "enum" in schema:
        parts.append("One of " + ", ".join(code(str(value)) for value in schema["enum"]) + ".")
    pattern = schema.get("pattern")
    if pattern and pattern != QUANTITY:
        parts.append(f"Pattern {code(pattern)}.")
    for low, high, unit in (
        ("minLength", "maxLength", "characters"),
        ("minItems", "maxItems", "items"),
        ("minProperties", "maxProperties", "keys"),
    ):
        text = _range(schema, low, high, unit)
        if text:
            parts.append(text)
    if "minimum" in schema or "maximum" in schema:
        minimum, maximum = schema.get("minimum"), schema.get("maximum")
        if minimum is not None and maximum is not None:
            parts.append(f"Range {minimum} to {maximum}.")
        elif minimum is not None:
            parts.append(f"Minimum {minimum}.")
        else:
            parts.append(f"Maximum {maximum}.")
    list_type = schema.get("x-kubernetes-list-type")
    if list_type == "set":
        parts.append("Unique items.")
    elif list_type == "map":
        keys = ", ".join(code(key) for key in schema.get("x-kubernetes-list-map-keys", []))
        parts.append(f"Unique by {keys}.")
    if schema.get("x-kubernetes-preserve-unknown-fields"):
        parts.append("Any fields.")
    return parts + _rules(schema)


def _rules(schema: dict) -> list[str]:
    return [
        f"Rule: {prose(rule['message']).rstrip('.')}." if rule.get("message") else f"Rule: {code(rule['rule'])}."
        for rule in schema.get("x-kubernetes-validations", [])
    ]


# Helm value tables ---------------------------------------------------------


def values_rows(relative: str, opaque: tuple[str, ...] = ()) -> list[Row]:
    """Return one row for each value of a ``values.yaml`` file.

    A mapping gets a row of its own only when it has a ``# --`` comment. The
    paths in ``opaque`` get one row for the whole mapping.
    """
    return _values_rows_from_node(source_nodes(relative), opaque)


def _values_rows_from_node(node: Node, opaque: tuple[str, ...]) -> list[Row]:
    rows: list[Row] = []
    _value_rows(node, "", set(opaque), rows)
    return rows


_DESCRIPTION = re.compile(r"#\s*--(?:\s|$)")


def _join(prefix: str, key: str) -> str:
    part = key if re.fullmatch(r"[A-Za-z0-9_-]+", key) else literal(key)
    return f"{prefix}.{part}" if prefix else part


def _value_rows(node: Node, prefix: str, opaque: set[str], rows: list[Row]) -> None:
    for key, child in node.value.items():
        path = _join(prefix, key)
        description = helm_docs_comment(child.comment)
        if isinstance(child.value, dict) and child.value and path not in opaque:
            if description:
                rows.append(Row(path, [""], description))
            _value_rows(child, path, opaque, rows)
        else:
            rows.append(Row(path, [value_default(child)], description))


def helm_docs_comment(comment: str) -> str:
    """Return the description of a ``# --`` comment block, as helm-docs does."""
    lines = comment.splitlines()
    starts = [number for number, line in enumerate(lines) if _DESCRIPTION.match(line)]
    if not starts:
        return ""
    first = _DESCRIPTION.sub("", lines[starts[-1]], count=1)
    rest = [re.sub(r"^#\s?", "", line) for line in lines[starts[-1] + 1:]]
    return " ".join(part.strip() for part in [first, *rest] if part.strip())


def value_default(node: Node) -> str:
    value = plain(node)
    if isinstance(value, dict):
        if not value:
            return code("{}")
        keys = [code(key) for key in value]
        shown = ", ".join(keys[:4]) + (f", and {len(keys) - 4} more" if len(keys) > 4 else "")
        return f"Keys: {shown}"
    if isinstance(value, list):
        if not value:
            return code("[]")
        if any(isinstance(item, (dict, list)) for item in value):
            return "1 entry" if len(value) == 1 else f"{len(value)} entries"
        text = literal(value)
        return code(text) if len(text) <= 60 else f"{len(value)} items"
    if isinstance(value, str) and "\n" in value.rstrip("\n"):
        return f"Text, {len(value.rstrip(chr(10)).splitlines())} lines"
    return code(literal(value))


# Page blocks ----------------------------------------------------------------


def _cells(line: str) -> list[str]:
    return [cell.strip() for cell in re.split(r"(?<!\\)\|", line.strip())[1:-1]]


def _key(cell: str) -> str:
    match = re.fullmatch(r"(`+) ?(.*?) ?\1", cell)
    return match.group(2).replace("\\|", "|") if match else cell


def page_descriptions(body: str) -> dict[str, str]:
    """Return the Description cells of the rows in a generated block."""
    result = {}
    for line in body.splitlines():
        if line.startswith("| `"):
            cells = _cells(line)
            result[_key(cells[0])] = cells[-1]
    return result


def render(table: Table, rows: list[Row], existing: dict[str, str]) -> str:
    lines = [
        "| " + " | ".join(table.columns) + " |",
        "| " + " | ".join("---" for _ in table.columns) + " |",
    ]
    seen = set()
    for row in rows:
        if row.key in seen:
            raise ValueError(f"{table.id}: duplicate row {row.key}")
        seen.add(row.key)
        cells = [code(row.key), *row.cells, existing.get(row.key) or prose(row.description)]
        lines.append("| " + " | ".join(cells) + " |")
    return "\n".join(lines)


def update(text: str, tables: list[Table]) -> str:
    """Rewrite the blocks of these tables in one page; each block must exist once."""
    by_id = {table.id: table for table in tables}
    found = dict.fromkeys(by_id, 0)

    def replace(match: re.Match) -> str:
        table = by_id.get(match.group("id"))
        if table is None:
            return match.group(0)
        found[table.id] += 1
        body = "\n" + render(table, table.build(), page_descriptions(match.group("body"))) + "\n\n"
        start, end = match.span("body")
        return match.group(0)[: start - match.start()] + body + match.group(0)[end - match.start():]

    text = BLOCK.sub(replace, text)
    for table in tables:
        if found[table.id] != 1:
            raise ValueError(f"{table.page}: expected one block '{table.id}', found {found[table.id]}")
    return text


FIELDS = ("Field", "Type", "Default", "Validation", "Description")
VALUES = ("Value", "Default", "Description")
CRDS = "reference/crds.md"
CHART_VALUES = "reference/chart-values.md"
HELM_PAGE = "reference/helm.md"


def crd_table(name: str, plural: str, root: str, collapse: tuple[str, ...] = ()) -> Table:
    source = crd_path(plural)
    return Table(name, CRDS, FIELDS, source, functools.partial(crd_rows, source, root, collapse))


def values_table(name: str, chart: str, opaque: tuple[str, ...] = ()) -> Table:
    source = f"{chart}/values.yaml"
    return Table(name, CHART_VALUES, VALUES, source, functools.partial(values_rows, source, opaque))


TABLES: list[Table] = [
    crd_table("agent-spec", "agents", "spec"),
    crd_table("agent-status", "agents", "status", ("plannedMigration",)),
    crd_table("team-spec", "teams", "spec"),
    crd_table("team-status", "teams", "status", ("compatibility.sandboxes[].issues",)),
    crd_table("sandbox-spec", "sandboxes", "spec"),
    crd_table("sandbox-status", "sandboxes", "status"),
    crd_table("agentpolicy-spec", "agentpolicies", "spec"),
    crd_table("agentpolicy-status", "agentpolicies", "status"),
    values_table("values-agent", "app/cetirun-chart", ("openclaw.config", "hermes.config", "ingress.annotations")),
    values_table("values-platform", "app/cetirun-platform"),
    values_table("values-operator", "app/cetirun-operator/chart", ("customRuntime.context",)),
    values_table("values-portal", "app/cetirun-portal/chart"),
]

# The level-2 headings of the Helm configuration page, with the chart of the
# release inventory and the path prefix of the values in their tables.
HELM_SECTIONS = {
    "Agent chart": ("cetirun", ""),
    "Platform chart": ("cetirun-platform", ""),
    "AI Gateway chart": ("cetirun-ai-gateway", ""),
    "Operator chart": ("cetirun-operator", ""),
    "Portal chart": ("cetirun-portal", ""),
    "Skill Marketplace values": ("cetirun-operator", "skillMarketplace."),
    "Observability chart": ("cetirun-observability", ""),
}


@functools.cache
def charts() -> dict[str, str]:
    """Return the directory of each chart of the release inventory."""
    inventory = json.loads((REPO / "scripts/release-artifacts.json").read_text(encoding="utf-8"))
    return {entry["name"]: entry["path"] for entry in inventory["charts"]}


@functools.cache
def dependencies(chart: str) -> tuple[str, ...]:
    metadata = plain(source_nodes(f"{charts()[chart]}/Chart.yaml"))
    return tuple(entry["name"] for entry in metadata.get("dependencies") or [])


@functools.cache
def declared_paths(chart: str) -> frozenset[str]:
    """Return the value paths of ``values.yaml`` and ``values.schema.json``."""
    root = charts()[chart]
    paths = set(_paths(plain(source_nodes(f"{root}/values.yaml")), lambda node: node))
    schema = REPO / root / "values.schema.json"
    if schema.is_file():
        properties = json.loads(schema.read_text(encoding="utf-8"))
        paths |= set(_paths(properties, lambda node: node.get("properties", {})))
    return frozenset(paths)


@functools.cache
def chart_paths(chart: str) -> frozenset[str]:
    """Return every value path that a chart accepts, with its parent paths.

    A chart accepts its own values, the values of each dependency below the
    name of the dependency, and the global values of each chart that depends
    on it, because Helm passes those to the dependency.
    """
    paths = set(declared_paths(chart))
    for dependency in dependencies(chart):
        paths.add(dependency)
        paths.update(f"{dependency}.{path}" for path in chart_paths(dependency))
    for parent in charts():
        if chart in dependencies(parent):
            paths.update(path for path in declared_paths(parent) if path.split(".")[0] == "global")
    return frozenset(paths)


def _paths(tree: object, children: Callable[[dict], dict], prefix: str = ""):
    if isinstance(tree, dict):
        for key, child in children(tree).items():
            path = _join(prefix, str(key))
            yield path
            yield from _paths(child, children, path)


def unknown_values(text: str, sections: dict[str, tuple[str, str]]) -> list[str]:
    """Return the value paths in the first column of curated tables that no chart accepts.

    ``sections`` maps a level-2 heading to a chart and a path prefix.
    """
    problems = []
    chart, prefix = None, ""
    for number, line in enumerate(text.splitlines(), start=1):
        heading = re.match(r"## (.+)", line)
        if heading:
            chart, prefix = sections.get(heading.group(1).strip(), (None, ""))
            continue
        if not chart or not line.startswith("| `"):
            continue
        for token in re.findall(r"`([^`]+)`", _cells(line)[0]):
            if f"{prefix}{token}" not in chart_paths(chart):
                problems.append(f"line {number}: {prefix}{token} is not a value of the {chart} chart")
    return problems


def unknown_blocks() -> list[str]:
    """Return the generated blocks of the pages that no table defines."""
    known = {table.id for table in TABLES}
    return [
        f"{path.relative_to(REPO)}: unknown generated block '{marker}'"
        for path in sorted(CONTENT.rglob("*.md"))
        for marker in MARKER.findall(path.read_text(encoding="utf-8"))
        if marker not in known
    ]


def generate() -> dict[Path, tuple[str, str]]:
    """Return the current and the generated text of each page with a table."""
    problems = unknown_blocks()
    if problems:
        raise ValueError("; ".join(problems))
    pages: dict[str, list[Table]] = {}
    for table in TABLES:
        pages.setdefault(table.page, []).append(table)
    result = {}
    for page, tables in pages.items():
        current = (CONTENT / page).read_text(encoding="utf-8")
        result[CONTENT / page] = (current, update(current, tables))
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--write", action="store_true", help="update the pages in place")
    args = parser.parse_args(argv)
    stale = []
    for path, (current, generated) in generate().items():
        if current == generated:
            continue
        if args.write:
            path.write_text(generated, encoding="utf-8")
            print(f"updated {path.relative_to(REPO)}")
        else:
            stale.append(path.relative_to(REPO).as_posix())
    for name in stale:
        print(f"{name}: generated tables are out of date; run {COMMAND}", file=sys.stderr)
    helm = (CONTENT / HELM_PAGE).read_text(encoding="utf-8")
    unknown = unknown_values(helm, HELM_SECTIONS)
    for problem in unknown:
        print(f"site/content/en/docs/{HELM_PAGE}: {problem}", file=sys.stderr)
    return 1 if stale or unknown else 0


if __name__ == "__main__":
    sys.exit(main())
