# Zero-Lock-In OTel Pipeline: Self-Hosted SigNoz Correlating Traces, Metrics, and Logs

OpenTelemetry instrumentation (traces, metrics, logs) in Go, Kubernetes/Helm deployment of a real observability backend, and reading the resulting correlated data the way SigNoz's product is designed to be used.

**Live demo:** https://signoz.ashanpraba.com

The demo runs entirely in the browser against seeded data — no API keys,
no accounts, and no external services required.

## Stack

- Go
- Kubernetes
- Helm
- AWS/local k8s (kind)

## How it works

- Kind create cluster; helm repo add signoz + helm install signoz/signoz with minimal single-node values.
- Write a small Go HTTP service with two endpoints (fast, artificially slow) instrumented via otel-go SDK: traces + custom duration metric + structured log lines shipped via OTLP to the signoz-otel-collector.
- Deployed the Go service into the same cluster with a bare k8s Deployment/Service manifest, port-forward the SigNoz UI.
- Run a load generator (hey or a bash curl loop) hitting both endpoints for ~30s to produce a visible P99 spike.
- Record: open SigNoz APM view showing the P99 spike, drill into a trace waterfall, click through to the correlated log line for that trace_id.

## Running locally

```bash
cd src
bash run.sh
```

Then open the printed URL. A prebuilt static version of the UI lives in
`src/web/` and can be opened directly with no server.
