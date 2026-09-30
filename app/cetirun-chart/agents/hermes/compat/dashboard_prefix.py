# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Apply the ingress-owned prefix at the Dashboard's HTTP response boundary."""

import html
from html.parser import HTMLParser
import re


MAX_DOCUMENT_BYTES = 8 * 1024 * 1024
_ATTR_URL = re.compile(
    r"""(\s(?:href|src|action|formaction|poster)\s*=\s*)(["'])(/[^"']*)\2""",
    re.IGNORECASE,
)
_CSS_URL = re.compile(r"""(url\(\s*["']?)(/[^)"'\s]+)""", re.IGNORECASE)


def public_path(path, prefix):
    """Prefix an origin-relative path once; leave external and relative URLs alone."""
    if not prefix or not path.startswith("/") or path.startswith("//"):
        return path
    if path == prefix or path.startswith((prefix + "/", prefix + "?", prefix + "#")):
        return path
    return prefix + path


def prefix_css(text, prefix):
    return _CSS_URL.sub(lambda match: match[1] + public_path(match[2], prefix), text)


class _PrefixedHTML(HTMLParser):
    def __init__(self, prefix):
        super().__init__(convert_charrefs=False)
        self.prefix = prefix
        self.parts = []
        self.in_style = False
        self.injected = False
        self.base_tag = f'<base href="{html.escape(prefix + "/", quote=True)}">'

    def handle_starttag(self, tag, attrs):
        if tag == "base":
            return
        text = _ATTR_URL.sub(
            lambda match: (
                match[1] + match[2] + public_path(match[3], self.prefix) + match[2]
            ),
            self.get_starttag_text(),
        )
        # Inline style attributes need the same handling as stylesheet bodies.
        self.parts.append(prefix_css(text, self.prefix))
        if tag == "head" and not self.injected:
            self.parts.append(self.base_tag)
            self.injected = True
        self.in_style = tag == "style"

    def handle_startendtag(self, tag, attrs):
        self.handle_starttag(tag, attrs)
        self.in_style = False

    def handle_endtag(self, tag):
        self.parts.append(f"</{tag}>")
        if tag == "style":
            self.in_style = False

    def handle_data(self, data):
        self.parts.append(prefix_css(data, self.prefix) if self.in_style else data)

    def handle_comment(self, data):
        self.parts.append(f"<!--{data}-->")

    def handle_decl(self, decl):
        self.parts.append(f"<!{decl}>")

    def handle_entityref(self, name):
        self.parts.append(f"&{name};")

    def handle_charref(self, name):
        self.parts.append(f"&#{name};")


def prefix_html(text, prefix):
    parser = _PrefixedHTML(prefix)
    parser.feed(text)
    parser.close()
    return ("" if parser.injected else parser.base_tag) + "".join(parser.parts)


class DashboardPrefixMiddleware:
    """Wrap HTTP documents without changing routing, auth, JSON, SSE, or WebSockets."""

    def __init__(self, app, *, normalise_prefix=None):
        if normalise_prefix is None:
            from hermes_cli.dashboard_auth.prefix import normalise_prefix
        self.app = app
        self.normalise_prefix = normalise_prefix

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        request_headers = dict(scope.get("headers", []))
        prefix = self.normalise_prefix(
            request_headers.get(b"x-forwarded-prefix", b"").decode("latin-1")
        )
        start = None
        chunks = []
        buffered = 0
        content_type = ""
        transform = False

        async def prefixed_send(message):
            nonlocal start, transform, content_type, buffered
            if message["type"] == "http.response.start":
                headers = [
                    (name, public_path(value.decode("latin-1"), prefix).encode("latin-1"))
                    if name.lower() == b"location" else (name, value)
                    for name, value in message.get("headers", [])
                ]
                fields = {name.lower(): value for name, value in headers}
                content_type = fields.get(b"content-type", b"").decode("latin-1").split(";")[0].strip().lower()
                transform = (
                    scope.get("method") != "HEAD"
                    and message["status"] not in (204, 304)
                    and content_type in ("text/html", "text/css")
                    and (prefix or content_type == "text/html")
                    and b"content-encoding" not in fields
                    and not fields.get(b"content-disposition", b"").lower().startswith(b"attachment")
                )
                start = {**message, "headers": headers}
                if not transform:
                    await send(start)
                return
            if not transform or message["type"] != "http.response.body":
                await send(message)
                return
            chunks.append(message.get("body", b""))
            buffered += len(chunks[-1])
            if buffered > MAX_DOCUMENT_BYTES:
                transform = False
                await send(start)
                await send({**message, "body": b"".join(chunks)})
                chunks.clear()
                return
            if message.get("more_body", False):
                return
            original = b"".join(chunks)
            try:
                text = original.decode("utf-8")
            except UnicodeDecodeError:
                await send(start)
                await send({**message, "body": original})
                return
            rewrite = prefix_html if content_type == "text/html" else prefix_css
            body = rewrite(text, prefix).encode("utf-8")
            headers = [
                (name, value) for name, value in start["headers"]
                if name.lower() not in (b"content-length", b"etag")
            ]
            headers.append((b"content-length", str(len(body)).encode()))
            headers.append((b"vary", b"X-Forwarded-Prefix"))
            await send({**start, "headers": headers})
            await send({**message, "body": body})

        await self.app(scope, receive, prefixed_send)
