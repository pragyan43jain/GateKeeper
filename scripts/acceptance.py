"""Run against a disposable deployment; creates test application and key records."""
import http.cookiejar
import json
import os
import ssl
import time
import urllib.error
import urllib.request

base = os.environ.get('GATEKEEPER_URL', 'http://localhost:18080').rstrip('/')
email = os.environ['DASHBOARD_ADMIN_EMAIL']
password = os.environ['DASHBOARD_ADMIN_PASSWORD']
handlers = [urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())]
if os.environ.get('GATEKEEPER_INSECURE_TLS') == '1':
    # Opt in only for disposable local stacks using Caddy's local development CA.
    handlers.append(urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
client = urllib.request.build_opener(*handlers)


def request(path, method='GET', body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={'Content-Type': 'application/json', **(headers or {})})
    try:
        response = client.open(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, response.read()


def expect(path, status, method='GET', body=None, headers=None):
    actual, response_headers, data = request(path, method, body, headers)
    assert actual == status, f'{method} {path}: expected {status}, got {actual}'
    return response_headers, data


expect('/api/keys', 401)
expect('/api/auth/login', 200, 'POST', {'email': email, 'password': password})
expect('/api/auth/me', 200)
_, raw = expect('/api/applications', 201, 'POST', {'name': 'acceptance-' + str(time.time_ns()), 'description': 'Disposable acceptance test'})
application = json.loads(raw)
_, raw = expect('/api/keys', 201, 'POST', {'applicationId': application['id'], 'name': 'acceptance', 'permissions': ['read'], 'requestsPerWindow': 3, 'windowSeconds': 60, 'burst': 0})
key = json.loads(raw)
headers = {'X-API-Key': key['secret']}
backend_ids = set()
for _ in range(3):
    reply, _ = expect('/api/students', 200, headers=headers)
    assert reply.get('X-Request-ID'), 'Missing request correlation'
    backend_ids.add(reply.get('X-Backend-Id'))
reply, _ = expect('/api/students', 429, headers=headers)
request_id = reply.get('X-Request-ID')
assert int(reply.get('Retry-After', '0')) > 0
assert len(backend_ids) == 2, f'Expected two upstreams, observed {backend_ids}'
_, raw = expect('/api/requests?search=' + request_id, 200)
records = json.loads(raw)['requests']
assert any(record['id'] == request_id and record['status'] == 429 for record in records)
assert key['secret'].encode() not in raw
expect('/api/metrics', 200)
expect('/metrics', 403)
expect('/api/keys/' + key['id'], 204, 'DELETE')
expect('/api/students', 401, headers=headers)

# Verify the key-level burst allowance extends, but does not replace, its window quota.
_, raw = expect('/api/keys', 201, 'POST', {'applicationId': application['id'], 'name': 'burst-acceptance', 'permissions': ['read'], 'requestsPerWindow': 1, 'windowSeconds': 60, 'burst': 2})
burst_key = json.loads(raw)
burst_headers = {'X-API-Key': burst_key['secret']}
for _ in range(3):
    expect('/api/students', 200, headers=burst_headers)
expect('/api/students', 429, headers=burst_headers)
expect('/api/keys/' + burst_key['id'], 204, 'DELETE')

# Verify the route-level burst policy independently of the consumer quota.
_, raw = expect('/api/backends', 200)
backends = json.loads(raw)['backends']
backend = next(item for item in backends if item.get('enabled') and not item.get('maintenance'))
_, raw = expect('/api/routes', 201, 'POST', {
    'name': 'acceptance route', 'pathPrefix': '/api/students', 'methods': ['GET'],
    'backendId': backend['configId'], 'authRequired': True, 'enabled': True,
    'requestsPerWindow': 1, 'windowSeconds': 60, 'burst': 2,
})
route = json.loads(raw)
_, raw = expect('/api/keys', 201, 'POST', {'applicationId': application['id'], 'name': 'route-burst-acceptance', 'permissions': ['read'], 'requestsPerWindow': 10, 'windowSeconds': 60, 'burst': 0})
route_key = json.loads(raw)
route_headers = {'X-API-Key': route_key['secret']}
for _ in range(3):
    expect('/api/students', 200, headers=route_headers)
expect('/api/students', 429, headers=route_headers)
expect('/api/routes/' + route['id'], 204, 'DELETE')
expect('/api/keys/' + route_key['id'], 204, 'DELETE')
_, raw = expect('/api/backend-health-history?backendId=' + backend['configId'] + '&limit=10', 200)
assert len(json.loads(raw)['events']) <= 10
expect('/api/auth/logout', 204, 'POST')
expect('/api/auth/me', 401)
print('PASS: sessions, key lifecycle, two-backend routing, key/route burst limits, Inspector correlation, protected metrics, logout')
