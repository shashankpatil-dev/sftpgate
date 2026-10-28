"""Records the webhooks SFTPgate sends, for the demo and test/e2e/run.sh.

POST /hook stores the call and checks its signature.
GET /calls returns every call so far, newest last.
"""
import hashlib
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SECRET = os.environ["WEBHOOK_SECRET"].encode()
calls = []


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        want = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
        calls.append({
            "payload": json.loads(body),
            "signature_ok": hmac.compare_digest(want, self.headers.get("X-Sftpgate-Signature", "")),
            "x_team": self.headers.get("X-Team"),
        })
        self.send_response(200)
        self.end_headers()

    def do_GET(self):
        out = json.dumps(calls, indent=2).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("", 9000), Handler).serve_forever()
