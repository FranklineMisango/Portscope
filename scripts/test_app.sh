#!/usr/bin/env bash
set -euo pipefail

# Simple smoke test script for local Docker Compose or k8s (kind/minikube)
MODE=${1:-compose} # compose or k8s
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "Running Portscope smoke tests in mode=${MODE}"

function wait_for() {
  local url=$1; local timeout=${2:-60}
  local start=$(date +%s)
  while true; do
    if curl -sSf "$url" >/dev/null 2>&1; then
      echo "OK: $url"
      return 0
    fi
    if (( $(date +%s) - start > timeout )); then
      echo "Timeout waiting for $url"
      return 1
    fi
    sleep 2
  done
}

function apply_migrations() {
  local migrations_dir="$ROOT_DIR/ingest/migrations"
  echo "Applying database migrations..."
  for migration in \
    "$migrations_dir/000_create_postgis_extension.sql" \
    "$migrations_dir/001_create_ais_messages.sql" \
    "$migrations_dir/002_create_normalized_tables.sql" \
    "$migrations_dir/003_create_ports_table.sql"
  do
    if [ ! -f "$migration" ]; then
      echo "Missing migration file: $migration" >&2
      exit 1
    fi
    (cd "$ROOT_DIR/ingest" && $COMPOSE_CMD exec -T db psql -U postgres -d postgres) < "$migration"
  done
}

if [ "$MODE" = "compose" ]; then
  echo "Starting docker-compose..."
  # detect docker-compose v1 or Docker CLI v2 'docker compose'
  if command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD="docker-compose"
  elif docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD="docker compose"
  else
    echo "Error: docker-compose or 'docker compose' not found. Install Docker Compose or the Docker CLI v2." >&2
    exit 1
  fi

  # ensure docker daemon is available
  if ! docker info >/dev/null 2>&1; then
    echo "Error: Docker daemon not running or not accessible. Start Docker and retry." >&2
    exit 1
  fi

  (cd "$ROOT_DIR/ingest" && $COMPOSE_CMD up -d --build)
  echo "Waiting for services..."
  for _ in $(seq 1 30); do
    if $COMPOSE_CMD exec -T db pg_isready -U postgres >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  apply_migrations
  wait_for http://localhost:8080/health 120
  wait_for http://localhost:3000/ 60 || true
else
  echo "Applying k8s manifests to current kubecontext..."
  kubectl apply -f "$ROOT_DIR/k8s/namespace.yaml"
  kubectl apply -f "$ROOT_DIR/k8s/postgres-deployment.yaml"
  kubectl apply -f "$ROOT_DIR/k8s/redis-deployment.yaml"
  kubectl apply -f "$ROOT_DIR/k8s/api-deployment.yaml"
  kubectl apply -f "$ROOT_DIR/k8s/consumer-deployment.yaml"
  kubectl apply -f "$ROOT_DIR/k8s/frontend-deployment.yaml"
  kubectl apply -f "$ROOT_DIR/k8s/ml-deployment.yaml"
  echo "Waiting for API..."
  SERVICE_IP=""
  echo "Waiting for API service endpoint (port 8080)..."
  # Try port-forward as fallback
  kubectl -n portscope get svc api >/dev/null 2>&1 && kubectl -n portscope port-forward svc/api 8080:8080 &
  wait_for http://localhost:8080/health 120
fi

# Basic API checks
echo "Checking /ports"
curl -fsS http://localhost:8080/ports | jq '.' || echo 'ports check failed'

# Pick a port id if exists
PORT_ID=$(curl -fsS http://localhost:8080/ports | jq -r '.[0].id' || echo "")
if [ -n "$PORT_ID" ]; then
  echo "Checking /port/$PORT_ID/live"
  curl -fsS "http://localhost:8080/port/$PORT_ID/live?radius=5000&mins=60" | jq '.' || echo 'live check failed'
  echo "Checking /port/$PORT_ID/traffic"
  curl -fsS "http://localhost:8080/port/$PORT_ID/traffic?range=30d" | jq '.' || echo 'traffic check failed'
else
  echo "No ports returned; skipping port-specific checks."
fi

echo "Checking Prometheus metrics endpoint"
curl -fsS --max-time 5 http://localhost:9090/metrics >/tmp/portscope-prometheus-metrics.txt && head -n 20 /tmp/portscope-prometheus-metrics.txt || echo 'prometheus metrics not reachable'

echo "Smoke tests completed"
