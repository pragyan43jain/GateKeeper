import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

const unexpected = new Rate('unexpected_responses');
const base = (__ENV.GATEKEEPER_URL || 'http://localhost:8080').replace(/\/$/, '');
const key = __ENV.GATEKEEPER_API_KEY;
if (!key) throw new Error('GATEKEEPER_API_KEY is required');

export const options = {
  vus: Number(__ENV.VUS || 10),
  duration: __ENV.DURATION || '30s',
  thresholds: {
    checks: ['rate>0.99'],
    unexpected_responses: ['rate<0.01'],
    http_req_duration: ['p(95)<1000'],
  },
};

export default function () {
  const response = http.get(`${base}/api/students`, { headers: { 'X-API-Key': key } });
  unexpected.add(response.status !== 200 && response.status !== 429);
  check(response, {
    'served or explicitly throttled': (r) => r.status === 200 || r.status === 429,
    'request is correlated': (r) => Boolean(r.headers['X-Request-Id']),
    'throttled requests advertise retry': (r) => r.status !== 429 || Number(r.headers['Retry-After']) > 0,
  });
  sleep(0.1);
}
