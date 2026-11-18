#!/usr/bin/env python3
"""A small stand-in for the Crew MCP server (spike items 10 and 71).

MCP over streamable HTTP, answered as plain JSON: initialize, tools/list, and
tools/call of one tool, crew_echo. A request without the header
"Authorization: Bearer <TOKEN>" gets 401. Every request is logged to standard
error with its method and whether the bearer matched, never the header itself.

  mcp-standin.py PORT TOKEN
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT, TOKEN = int(sys.argv[1]), sys.argv[2]
TOOL = {
    "name": "crew_echo",
    "description": "Echo a text back, prefixed with crew-echo. Use it when asked to call crew_echo.",
    "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}},
                    "required": ["text"]},
}


def log(*a):
    print(*a, file=sys.stderr, flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, code, body=None):
        data = b"" if body is None else json.dumps(body).encode()
        self.send_response(code)
        if body is not None:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def authed(self):
        return self.headers.get("Authorization") == f"Bearer {TOKEN}"

    def do_GET(self):
        log(f"GET {self.path} bearer={'ok' if self.authed() else 'BAD'}")
        self.reply(405)

    def do_DELETE(self):
        self.reply(200)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        msg = json.loads(self.rfile.read(n) or b"{}")
        ok = self.authed()
        method = msg.get("method")
        log(f"POST {self.path} {method} bearer={'ok' if ok else 'BAD'}")
        if not ok:
            return self.reply(401, {"error": "unauthorized"})
        if "id" not in msg:
            return self.reply(202)
        rid = msg["id"]
        if method == "initialize":
            ver = msg.get("params", {}).get("protocolVersion", "2025-06-18")
            res = {"protocolVersion": ver, "capabilities": {"tools": {}},
                   "serverInfo": {"name": "crew-standin", "version": "0"}}
        elif method == "tools/list":
            res = {"tools": [TOOL]}
        elif method == "tools/call":
            text = msg.get("params", {}).get("arguments", {}).get("text", "")
            log(f"  crew_echo called with {len(text)} characters")
            res = {"content": [{"type": "text", "text": f"crew-echo: {text}"}]}
        elif method == "ping":
            res = {}
        else:
            return self.reply(200, {"jsonrpc": "2.0", "id": rid,
                                    "error": {"code": -32601, "message": "not found"}})
        self.reply(200, {"jsonrpc": "2.0", "id": rid, "result": res})


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
