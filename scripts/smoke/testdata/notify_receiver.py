#!/usr/bin/env python3
"""Loopback webhook receiver for smoke 10. Appends one JSON line per request.

usage: notify_receiver.py PORT LOG FAILFILE

Each line: {"path", "headers" (the X-PBPanel-* ones), "body" (base64)}. While
FAILFILE exists the receiver answers 500, so a retry test can keep a delivery
failing; removing the file lets the next retry succeed.
"""
import base64
import http.server
import json
import os
import sys

PORT = int(sys.argv[1])
LOG = sys.argv[2]
FAILFILE = sys.argv[3]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        record = {
            "path": self.path,
            "headers": {k: v for k, v in self.headers.items() if k.lower().startswith("x-pbpanel")},
            "body": base64.b64encode(body).decode(),
        }
        with open(LOG, "a") as f:
            f.write(json.dumps(record) + "\n")
        code = 500 if os.path.exists(FAILFILE) else 200
        self.send_response(code)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
