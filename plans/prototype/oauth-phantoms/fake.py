import base64
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])
LOG = open(sys.argv[2], "a", buffering=1)
CLAIMS = sys.argv[3]
TOKEN_MODE = os.environ.get("FAKE_TOKEN_MODE", "ok")
API_MODE = os.environ.get("FAKE_API_MODE", "deny")
RECORDED = ("authorization", "x-api-key", "chatgpt-account-id", "anthropic-beta", "content-type")


def b64(d):
    return base64.urlsafe_b64encode(json.dumps(d).encode()).rstrip(b"=").decode()


def phantom_jwt(claims):
    return b64({"alg": "RS256", "typ": "JWT"}) + "." + b64(claims) + ".hsh_" + "a" * 64


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def handle_any(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n).decode(errors="replace") if n else ""
        headers = {k: v for k, v in self.headers.items() if k.lower() in RECORDED}
        LOG.write(json.dumps({"t": time.time(), "m": self.command, "p": self.path, "h": headers, "b": body[:600]}) + "\n")
        if self.path.endswith("/oauth/token"):
            if TOKEN_MODE == "invalid_grant":
                return self.reply(400, {"error": "invalid_grant", "error_description": "fake"})
            claims = json.load(open(CLAIMS))
            claims["exp"] = int(time.time()) + 3600
            return self.reply(200, {
                "access_token": phantom_jwt(claims),
                "id_token": phantom_jwt(claims),
                "refresh_token": "hsh_" + "b" * 64,
                "expires_in": 3600,
            })
        if API_MODE == "ok" and "/responses" not in self.path and "/messages" not in self.path:
            return self.reply(200, {})
        self.reply(401, {"error": {"type": "authentication_error", "message": "fake server: recorded request"}})

    def reply(self, code, obj):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_DELETE = handle_any


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
