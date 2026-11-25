#!/usr/bin/env python3
"""Checks the signature of the last request the webhook receiver recorded.

usage: notify_verify.py LOG KEY [INDEX]

Recomputes X-PBPanel-Signature as sha256=HMAC-SHA256(KEY, timestamp + "." + body)
with Python's hmac module, independent of the panel's Go code. INDEX picks a
record from the end (default 0 = the last one). Prints the record's event id
and kind on success. Exits 0 on a match, 1 otherwise.
"""
import base64
import hashlib
import hmac
import json
import sys

LOG, KEY = sys.argv[1], sys.argv[2].encode()
INDEX = int(sys.argv[3]) if len(sys.argv) > 3 else 0

with open(LOG) as f:
    records = [json.loads(line) for line in f if line.strip()]
if len(records) <= INDEX:
    print("no record")
    sys.exit(1)

rec = records[-1 - INDEX]
# Go canonicalises header names on the wire (X-Pbpanel-...), so match them case-insensitively.
headers = {k.lower(): v for k, v in rec["headers"].items()}
body = base64.b64decode(rec["body"])
ts = headers.get("x-pbpanel-timestamp", "")
want = "sha256=" + hmac.new(KEY, ts.encode() + b"." + body, hashlib.sha256).hexdigest()
got = headers.get("x-pbpanel-signature", "")
if got != want:
    print("MISMATCH got=%s want=%s" % (got, want))
    sys.exit(1)
payload = json.loads(body)
print("OK event=%s kind=%s resolved=%s" % (payload.get("id"), payload.get("kind"), payload.get("resolved")))
