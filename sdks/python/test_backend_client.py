import io
import json
from pathlib import Path
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from backend_client import BackendError, Client, ProtocolError, sse_events

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_GET(self):
        if self.path == "/redirect":
            self.send_response(302); self.send_header("Location", "/target"); self.end_headers(); return
        self.send_response(429); self.end_headers(); self.wfile.write(b'{"error":{"code":"rate_limited"}}')
    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers()
        self.wfile.write(b'data: {"choices":[{"delta":{"content":"hello"}}]}\n\ndata: [DONE]\n\n')

class SDKTests(unittest.TestCase):
    def test_fragmented_utf8_crlf_and_multiline(self):
        class OneByte(io.BytesIO):
            def read(self, n=-1):
                return super().read(1)
        raw = '\ufeff: heartbeat\r\ndata: 中文\rdata: second\r\r'.encode()
        self.assertEqual(list(sse_events(OneByte(raw))), ["中文\nsecond"])
    def test_limits_and_partial_eof(self):
        for raw in [b"data: [DONE]\n", b"data: [DONE]", b":" + b"x" * (128 << 10) + b"\n\n", b"data: \xff\n\n"]:
            with self.assertRaises(ProtocolError):
                list(sse_events(io.BytesIO(raw)))
    def test_live_stream_errors_and_redirect(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever); thread.start()
        try:
            with tempfile.TemporaryDirectory() as temporary:
                key = Path(temporary) / "key"; key.write_text("public-test-key")
                client = Client("http://127.0.0.1:" + str(server.server_port), key)
                with client.chat([{"role": "user", "content": "hello"}]) as chunks:
                    self.assertEqual(list(chunks)[0]["choices"][0]["delta"]["content"], "hello")
                # A cloud image request can exceed the old 128KiB SDK bound;
                # only chat receives the larger transport allowance.
                parts = [{"type": "image_url", "image_url": {"url": "data:image/png;base64," + "A" * (150 << 10)}}]
                with client.chat([{"role": "user", "content": parts}]) as chunks:
                    self.assertEqual(list(chunks)[0]["choices"][0]["delta"]["content"], "hello")
                with self.assertRaises(ValueError):
                    client.request("POST", "/v1/embeddings", {"input": "x" * (150 << 10)})
                with self.assertRaises(BackendError) as e:
                    client.balance()
                self.assertEqual((e.exception.status, e.exception.code), (429, "rate_limited"))
                with self.assertRaises(BackendError) as e:
                    client.request("GET", "/redirect")
                self.assertEqual(e.exception.code, "redirect_refused")
                for path in ["//other", "/../secret", "https://other"]:
                    with self.assertRaises(ValueError):
                        client.request("GET", path)
        finally:
            server.shutdown(); thread.join(); server.server_close()

if __name__ == "__main__":
    unittest.main()
