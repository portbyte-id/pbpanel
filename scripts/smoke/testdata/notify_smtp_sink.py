#!/usr/bin/env python3
"""Loopback SMTP sink for smoke 10: plain SMTP with AUTH PLAIN, no TLS.

usage: notify_smtp_sink.py PORT LOG

Each accepted message is appended to LOG as its raw text followed by a line
"==END==". Each AUTH is appended as "AUTH <user>". Accepts any credentials;
the smoke checks what the panel sent, not whether the sink was strict.
"""
import base64
import socketserver
import sys

PORT = int(sys.argv[1])
LOG = sys.argv[2]


class Handler(socketserver.StreamRequestHandler):
    def send(self, text):
        self.wfile.write((text + "\r\n").encode())
        self.wfile.flush()

    def handle(self):
        self.send("220 sink ready")
        in_data = False
        lines = []
        while True:
            raw = self.rfile.readline()
            if not raw:
                return
            line = raw.decode(errors="replace").rstrip("\r\n")
            if in_data:
                if line == ".":
                    in_data = False
                    with open(LOG, "a") as f:
                        f.write("\n".join(lines) + "\n==END==\n")
                    lines = []
                    self.send("250 queued")
                else:
                    lines.append(line)
                continue
            verb = line.split(" ", 1)[0].upper()
            if verb in ("EHLO", "HELO"):
                self.send("250-sink")
                self.send("250 AUTH PLAIN")
            elif verb == "AUTH":
                parts = line.split()
                try:
                    fields = base64.b64decode(parts[-1]).split(b"\x00")
                    user = fields[1].decode(errors="replace") if len(fields) > 1 else ""
                except Exception:
                    user = ""
                with open(LOG, "a") as f:
                    f.write("AUTH " + user + "\n")
                self.send("235 ok")
            elif verb in ("MAIL", "RCPT"):
                self.send("250 ok")
            elif verb == "DATA":
                in_data = True
                self.send("354 go")
            elif verb == "QUIT":
                self.send("221 bye")
                return
            else:
                self.send("500 unknown command")


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True


Server(("127.0.0.1", PORT), Handler).serve_forever()
