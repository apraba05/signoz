#!/usr/bin/env bash
# One-command demo: kind + SigNoz Helm + OTel-instrumented Go app + latency spike.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER=signoz-demo
CTX="kind-${CLUSTER}"
NS=platform
IMAGE=demo-app:local
APP_PF_PID=""
UI_PF_PID=""

k() { kubectl --context "${CTX}" "$@"; }

for command in docker go kind kubectl helm curl; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "Missing required command: ${command}" >&2
    exit 1
  fi
done

if ! docker info >/dev/null 2>&1; then
  if sg docker -c 'docker info' >/dev/null 2>&1; then
    exec sg docker -c "\"$0\" $*"
  fi
  echo "Docker is required (and your user must reach the docker socket)." >&2
  exit 1
fi

cleanup() {
  [[ -n "${APP_PF_PID}" ]] && kill "${APP_PF_PID}" 2>/dev/null || true
  [[ -n "${UI_PF_PID}" ]] && kill "${UI_PF_PID}" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> Go build (sanity check)"
go build -o /tmp/demo-app-check .
rm -f /tmp/demo-app-check

echo "==> Build demo-app image"
docker build -t "${IMAGE}" .

echo "==> kind cluster (${CLUSTER})"
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  echo "    (reusing existing cluster)"
else
  kind create cluster --name "${CLUSTER}" --wait 120s
fi
kubectl config use-context "${CTX}" >/dev/null
k cluster-info >/dev/null
kind load docker-image "${IMAGE}" --name "${CLUSTER}"

echo "==> Helm install SigNoz (namespace ${NS})"
helm repo add signoz https://charts.signoz.io >/dev/null
helm repo update signoz >/dev/null
if helm status signoz -n "${NS}" >/dev/null 2>&1; then
  echo "    (upgrade existing release)"
  helm upgrade signoz signoz/signoz -n "${NS}" -f signoz-values.yaml --wait --timeout 15m
else
  helm install signoz signoz/signoz -n "${NS}" --create-namespace -f signoz-values.yaml --wait --timeout 15m
fi

echo "==> Deploy demo-app"
k apply -f k8s/demo-app.yaml
k rollout status deploy/demo-app --timeout=120s

echo "==> Port-forward SigNoz UI (:3301) and demo-app (:8080)"
k -n "${NS}" port-forward svc/signoz 3301:8080 >/tmp/signoz-ui-pf.log 2>&1 &
UI_PF_PID=$!
k port-forward svc/demo-app 8080:8080 >/tmp/demo-app-pf.log 2>&1 &
APP_PF_PID=$!
sleep 3

echo "==> Warm /fast and /slow"
curl -sf "http://127.0.0.1:8080/fast" >/dev/null
curl -sf "http://127.0.0.1:8080/slow" >/dev/null

echo "==> Load generate ~30s (mostly /fast, some /slow → visible P99 spike)"
(
  end=$((SECONDS + 30))
  while (( SECONDS < end )); do
    curl -sf "http://127.0.0.1:8080/fast" >/dev/null &
    if (( RANDOM % 5 == 0 )); then
      curl -sf "http://127.0.0.1:8080/slow" >/dev/null &
    fi
    # Keep concurrency modest so the tiny node stays healthy.
    if (( $(jobs -rp | wc -l) > 8 )); then
      wait -n || true
    fi
    sleep 0.05
  done
  wait
)

echo
echo "Ready."
echo "  SigNoz UI:  http://127.0.0.1:3301"
echo "  demo-app:   http://127.0.0.1:8080/fast | /slow"
echo
echo "Record:"
echo "  1. Open Services → demo-app → Latency / P99 (spike from /slow)."
echo "  2. Open a /slow trace waterfall."
echo "  3. Jump to the correlated log line for that trace_id."
echo
echo "Press Ctrl-C when the recording is done; this stops both port-forwards."
echo "Tear down with: kind delete cluster --name ${CLUSTER}"

# Keep the one-command demo alive while the browser uses both port-forwards.
wait "${UI_PF_PID}" "${APP_PF_PID}"
