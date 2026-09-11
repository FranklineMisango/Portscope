#!/usr/bin/env bash
set -e

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${CYAN}[portscope]${NC} $1"; }
ok()    { echo -e "${GREEN}[portscope]${NC} $1"; }
warn()  { echo -e "${YELLOW}[portscope]${NC} $1"; }

AIS_KEY="${AISSTREAM_API_KEY:-$(grep AISSTREAM_API_KEY .env 2>/dev/null | cut -d= -f2)}"

info "══════════════════════════════════════════════════════"
info "  Portscope — Maritime Intelligence Dashboard"
info "══════════════════════════════════════════════════════"

# ─── Check Docker ───────────────────────────────────────
DOCKER_OK=false
if docker info &>/dev/null; then
  DOCKER_OK=true
  info "Docker detected"
else
  warn "Docker not running — will start in limited mode"
fi

# ─── 1. Postgres ─────────────────────────────────────────
if $DOCKER_OK; then
  info "Step 1/5 — Starting Postgres..."
  docker compose -f ingest/docker-compose.yml up -d db db-migrate 2>&1
  for i in $(seq 1 15); do
    if docker compose -f ingest/docker-compose.yml exec -T db pg_isready -U postgres &>/dev/null; then
      ok "Postgres ready"
      break
    fi
    sleep 1
  done

  # ─── 2. ArcGIS sync ──────────────────────────────────
  info "Step 2/5 — Syncing ArcGIS data..."
  docker compose -f ingest/docker-compose.yml up -d dataset-sync 2>&1
  info "ArcGIS sync started (runs every 6h)"

  # ─── 3. AIS streamer ─────────────────────────────────
  if [ -n "$AIS_KEY" ]; then
    info "Step 3/5 — Starting AIS streamer..."
    AISSTREAM_API_KEY="$AIS_KEY" docker compose -f ingest/docker-compose.yml up -d aisstreamer 2>&1
    ok "AIS streamer started"
  else
    warn "Step 3 — No AIS key, skipping streamer"
  fi
else
  warn "Steps 1-3 skipped — limited mode (industries only)"
fi

# ─── 4. Backend ──────────────────────────────────────────
info "Step 4/5 — Starting backend..."
# Free port 8000
lsof -ti tcp:8000 2>/dev/null | xargs kill -9 2>/dev/null || true
sleep 1

PYTHON="python3"
[ -n "$(command -v conda)" ] && PYTHON="python"

cd backend
$PYTHON -m uvicorn main:app --host 0.0.0.0 --port 8000 &
BACKEND_PID=$!
cd "$ROOT"
sleep 2
kill -0 $BACKEND_PID 2>/dev/null && ok "Backend on :8000" || warn "Backend failed"

# ─── 5. Frontend ────────────────────────────────────────
info "Step 5/5 — Starting frontend (http://localhost:5173)..."
cd frontend
[ -f node_modules/.package-lock.json ] || npm install --silent 2>/dev/null
npm run dev -- --host 0.0.0.0 &
FRONTEND_PID=$!
cd "$ROOT"

ok "══════════════════════════════════════════════════════"
ok "  Portscope running!"
echo ""
ok "  Frontend : http://localhost:5173"
ok "  Backend  : http://127.0.0.1:8000"
ok "  API docs : http://127.0.0.1:8000/docs"
echo ""
ok "  Click any port on the globe to see analytics"
ok "══════════════════════════════════════════════════════"
echo ""
info "Press Ctrl+C to stop everything"

trap "info 'Shutting down...'; kill $BACKEND_PID $FRONTEND_PID 2>/dev/null; docker compose -f ingest/docker-compose.yml stop 2>/dev/null; exit 0" INT TERM
wait