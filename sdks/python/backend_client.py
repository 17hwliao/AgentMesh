"""Dependency-free gateway and SQL Sentinel SDK. Use private credential files."""
from contextlib import contextmanager
import json
from pathlib import Path
import ssl
import urllib.error
import urllib.parse
import urllib.request

MAX_EVENT = 128 << 10
MAX_RESPONSE = 4 << 20

class BackendError(Exception):
    def __init__(self, status, code):
        self.status, self.code = status, code
        super().__init__("backend HTTP " + str(status) + ": " + code)

class ProtocolError(Exception):
    pass

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise BackendError(code, "redirect_refused")

def sse_events(source):
    """Decode UTF-8 SSE with LF/CRLF/CR, bounded lines/events/response and strict EOF."""
    line, data, event_size, total, skip_lf, first = bytearray(), [], 0, 0, False, True
    read = getattr(source, "read1", source.read)
    while True:
        block = read(4096)
        if not block:
            if line or data:
                raise ProtocolError("truncated SSE event")
            return
        total += len(block)
        if total > MAX_RESPONSE:
            raise ProtocolError("response limit")
        for byte in block:
            if skip_lf:
                skip_lf = False
                if byte == 10:
                    continue
            if byte not in (10, 13):
                line.append(byte)
                if len(line) > MAX_EVENT:
                    raise ProtocolError("SSE line limit")
                continue
            skip_lf = byte == 13
            try:
                text = line.decode("utf-8")
            except UnicodeError as e:
                raise ProtocolError("invalid UTF-8") from e
            line.clear()
            if first:
                text, first = text.removeprefix("\ufeff"), False
            if not text:
                if data:
                    yield "\n".join(data)
                data, event_size = [], 0
                continue
            event_size += len(text.encode()) + 1
            if event_size > MAX_EVENT:
                raise ProtocolError("SSE event limit")
            if text.startswith("data:"):
                value = text[5:]
                data.append(value[1:] if value.startswith(" ") else value)

class Client:
    def __init__(self, base_url, token_file, *, ca_file=None, timeout=30):
        parsed = urllib.parse.urlsplit(base_url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError("invalid explicit base URL")
        if timeout <= 0 or timeout > 120:
            raise ValueError("timeout must be 0..120 seconds")
        self.base_url = base_url.rstrip("/")
        with Path(token_file).open("r",encoding="utf-8-sig") as key:
            raw=key.read(16385).strip()
        if not raw or len(raw) > 16384 or any(c.isspace() or not 33 <= ord(c) <= 126 for c in raw):
            raise ValueError("invalid private credential file")
        self._token, self.timeout = raw, timeout
        context = ssl.create_default_context(cafile=str(ca_file) if ca_file else None)
        self._opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=context))

    def _open(self, method, path, body=None):
        if not path.startswith("/") or path.startswith("//") or ".." in path or "#" in path:
            raise ValueError("relative API path required")
        data = json.dumps(body, ensure_ascii=False).encode() if body is not None else None
        if data is not None and len(data) > ((2 << 20) if path == "/v1/chat/completions" else (128 << 10)):
            raise ValueError("request body limit")
        request = urllib.request.Request(self.base_url + path, data, {"Authorization": "Bearer " + self._token, "Content-Type": "application/json"}, method=method)
        try:
            return self._opener.open(request, timeout=self.timeout)
        except urllib.error.HTTPError as e:
            with e:
                raw = e.read(MAX_EVENT + 1)
            code = "http_error"
            try:
                error = json.loads(raw)
                value = error.get("code", error.get("error", "http_error"))
                if isinstance(value, dict):
                    value = value.get("code", "http_error")
                if isinstance(value, str) and len(value) <= 128 and all(c.isalnum() or c in "_-" for c in value):
                    code = value
            except (ValueError, AttributeError):
                pass
            raise BackendError(e.code, code) from None

    def request(self, method, path, body=None):
        with self._open(method, path, body) as response:
            raw = response.read(MAX_RESPONSE + 1)
            if len(raw) > MAX_RESPONSE:
                raise ProtocolError("response limit")
            if not raw:
                return None
            try:
                return json.loads(raw)
            except (ValueError, UnicodeError) as e:
                raise ProtocolError("invalid JSON response") from e

    @contextmanager
    def chat(self, messages, *, model="domestic-chat", max_tokens=100):
        response = self._open("POST", "/v1/chat/completions", {"model": model, "messages": messages, "max_tokens": max_tokens, "temperature": 0, "stream": True, "stream_options": {"include_usage": True}})
        def chunks():
            for event in sse_events(response):
                if event.strip() == "[DONE]":
                    return
                try:
                    value = json.loads(event)
                except ValueError as e:
                    raise ProtocolError("invalid stream JSON") from e
                if not isinstance(value, dict) or value.get("error"):
                    raise ProtocolError("upstream stream error")
                yield value
            raise ProtocolError("stream ended without DONE")
        try:
            if "text/event-stream" not in response.headers.get("Content-Type", ""):
                raise ProtocolError("unexpected stream content type")
            yield chunks()
        finally:
            response.close()

    def balance(self):
        return self.request("GET", "/v1/billing/balance")

    def submit_job(self, delivery_id, sql, candidate_spec, *, pr_number=1):
        return self.request("POST", "/jobs", {"delivery_id": delivery_id, "pr_number": pr_number, "sql": sql, "candidate_spec": candidate_spec})

    def job(self, job_id):
        return self.request("GET", "/jobs/" + urllib.parse.quote(job_id, safe=""))

    def rag_query(self, question, *, top_k=3):
        return self.request("POST", "/rag/query", {"question": question, "top_k": top_k})
