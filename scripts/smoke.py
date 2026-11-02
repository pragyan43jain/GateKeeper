#!/usr/bin/env python3
"""Verify a running gateway; uses an isolated key's existing rate-limit quota."""
import json
import os
import ssl
import urllib.request
import urllib.error

base = os.environ.get('GATEKEEPER_URL', 'https://localhost').rstrip('/')
key = os.environ.get('GATEKEEPER_API_KEY')
if not key:
    raise SystemExit('Set GATEKEEPER_API_KEY before running the smoke check')
context = ssl._create_unverified_context() if os.environ.get('ALLOW_LOCAL_TLS') == '1' else ssl.create_default_context()

def request(path, authenticated=False):
    req = urllib.request.Request(base + path, headers={'X-API-Key': key} if authenticated else {})
    try:
        response = urllib.request.urlopen(req, context=context, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, response.read().decode()

assert request('/health')[0] == 200, 'Health failed'
assert request('/api/students')[0] == 401, 'Authentication failed'
status, headers, body = request('/api/students', True)
assert status == 200, f'API returned {status}; wait for rate-limit reset if 429'
assert json.loads(body)['students'], 'Missing student response'
assert headers.get('X-Backend-Id'), 'Missing backend identity'
assert headers.get('X-RateLimit-Limit'), 'Missing rate limit headers'
status, _, body = request('/metrics')
assert status == 200 and 'gateway_requests_total' in body, 'Metrics unavailable'
print('PASS: health, authentication, upstream response, rate-limit headers, metrics')
