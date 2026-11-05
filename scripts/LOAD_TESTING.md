# Load testing

Run against an isolated local or staging deployment with a dedicated consumer key:

```sh
GATEKEEPER_URL=https://localhost GATEKEEPER_API_KEY=your-test-key k6 run scripts/load.js
```

Use a trusted certificate. Configure `VUS` and `DURATION` to change concurrency
and run length. The default is ten virtual users for thirty seconds. This suite
treats explicit 429 throttling as an expected policy outcome, requires request
correlation and retry headers, rejects unexpected responses, and bounds P95
latency to one second. It does not establish a throughput capacity claim.

To measure throughput without throttling, create a dedicated key with a quota
above the offered load and adjust the selected route's policy accordingly.
Observe backend distribution, latency percentiles, Redis memory, and CPU while
increasing load gradually. Do not run this suite against production by default.

Prometheus loads `prometheus/alerts.yml`. Rules cover unreachable gateways,
unhealthy backends, Redis errors, and sustained server error rates. They are
visible in Prometheus; notification delivery requires an operator-configured
Alertmanager and receiver. No external notifications are configured automatically.
