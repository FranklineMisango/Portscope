

# Portscope

Comprehensive platform to explore port activity, chokepoints, and maritime traffic analytics.

[![Go](https://img.shields.io/badge/Go-1.20-blue?logo=go)](https://golang.org/)
[![Python](https://img.shields.io/badge/Python-3.11-blue?logo=python)](https://www.python.org/)
[![JavaScript](https://img.shields.io/badge/JavaScript-ES6-yellow?logo=javascript)](https://developer.mozilla.org/en-US/docs/Web/JavaScript)
[![Docker](https://img.shields.io/badge/Docker-compatible-blue?logo=docker)](https://www.docker.com/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-ready-blue?logo=kubernetes)](https://kubernetes.io/)
[![Helm](https://img.shields.io/badge/Helm-charts-lightgrey?logo=helm)](https://helm.sh/)
[![Postgres](https://img.shields.io/badge/Postgres-PostGIS-blue?logo=postgresql)](https://www.postgresql.org/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

---

## Overview

`Portscope` ingests vessel and port geometry data, stores it in PostGIS, and provides an interactive web frontend and analytics services for exploring global port activity and chokepoint intensity.

Key capabilities:
- Interactive mapping and drill-down metrics
- Time-series and heatmap visualizations
- Containerized microservices (Go, Python, Node.js)
- Scalable deployment via Docker & Helm charts for Kubernetes
- Monitoring with Prometheus and Grafana

## Architecture

Below is a simplified architecture view. The repo contains deployable artifacts for each component.


```
mermaid
flowchart LR
  F[Frontend] --> API[API Gateway]
  API --> DB[Postgres]
  API --> Cache[Redis]
  Ingest[Ingest Services] --> DB
  ML[ML Service] --> API
  Monitoring[Monitoring] --> API
  subgraph Infra
    DB
    Cache
  end
```

## Components (repo folders)
- `api/` — Go API, gateway and portwatch logic.
- `ingest/` — Ingestion pipelines: AIS streamer, ArcGIS sync, normalizer, migrations.
- `frontend/` — Single-page web app (Leaflet/Mapbox usage) served by simple static server.
- `ml/` — Python microservice for forecasting and analytics models.
- `charts/portscope` — Helm chart(s) and Kubernetes templates for deployment.
- `portwatch_scraper/` — Python scraper and normalizer for smaller datasets.

## Data sources (what's checked in and where data comes from)

Local checked-in data (in `data/`):
- `data/Ports.csv` — tabular port metadata used for index and lookups.
- `data/Ports.geojson` — port polygons and centroids (GeoJSON).
- `data/Chokepoints.geojson` — chokepoint polygon or point features.
- `data/PortWatch_chokepoints_database.csv` — auxiliary portwatch chokepoint dataset.

Ingested/streamed sources and feeds (configured in `ingest/`):
- AIS streaming (real-time vessel tracks) — implemented under `ingest/aisstreamer` and `ingest/consumer`.
- ArcGIS feature services — periodic sync implemented by `ingest/arcgis_sync.go`.

Notes on provenance: the checked-in `data/` files are snapshots used for demos and local development. Production ingestion (AIS/ArcGIS) should be configured with credentials and proper source endpoints; see `ingest/README` and the ingestion config files for more details.

## API (summary)

Primary endpoints (see `api/` for implementation and full docs):
- `GET /ports` — ports list / geojson feed
- `GET /chokepoints` — chokepoints list / geojson feed
- `GET /port/{id}` — port metadata and geometry
- `GET /port/{id}/traffic?range=30d` — historical traffic summary
- `GET /port/{id}/forecast` — model forecast (if ML enabled)

## Quick Start — Local Development

Prerequisites:
- `docker` and `docker-compose`
- `go` (for local builds) — optional if using images
- `python3` (for ML or scraper) — optional

Recommended: run the repository root from `/home/misango/codechest/Portscope`.

1) Start core services using Docker Compose (local dev stack):

```bash
docker compose -f ingest/docker-compose.yml up --build
```

2) Access services locally:
- Frontend: http://localhost:3000
- API: http://localhost:8080
- Grafana: http://localhost:3001 (prometheus/grafana configured in `monitoring/`)

3) Run migrations (if you prefer to apply them manually):

```bash
# from repo root
psql "postgresql://postgres:postgres@localhost:5432/portscope" -f ingest/migrations/000_create_postgis_extension.sql
psql "postgresql://postgres:postgres@localhost:5432/portscope" -f ingest/migrations/001_create_ais_messages.sql
# run remaining migration files in order
```

## Development notes

- API: `api/main.go` is the entrypoint. Use `go build` and run for local debugging.
- Ingest: `ingest/main.go` and subfolders contain streamers and syncers. See `ingest/Dockerfile` variants for container builds.
- Frontend: `frontend/index.html` and `frontend/app.js` are minimal and served by a static image; edit and reload the container for changes.
- ML: `ml/app.py` is a lightweight Flask app exposing model endpoints; `ml/requirements.txt` lists Python deps.

## Deployment

- Helm charts live in `charts/portscope`. Example install (cluster must be configured):

```bash
helm upgrade --install portscope charts/portscope -n portscope --create-namespace
```

- Kubernetes manifests for quick deploy are provided in `k8s/`.

## Monitoring & Observability

- Prometheus config: `monitoring/prometheus/prometheus.yml` (and `k8s/prometheus-configmap.yaml`).
- Grafana dashboards and provisioning live under `monitoring/grafana/`.

## Contributing

Please open issues and PRs. For local testing, prefer running via `docker compose` to reproduce services and the local Postgres/PostGIS database.

## Future Updates

### 1. Port Congestion & Waiting Time Analytics

Before ships actually pull into a berth to unload, they often sit in a designated "anchorage zone" outside the port for days if the port is backed up.

* **How it works:** You define a polygon around the anchorage zone. When a ship enters that polygon and its `speed` drops to near 0, you log an anchorage start time. When it moves toward a berth, you log the end time.
* **Value:** You can calculate the average **Turnaround Time (TaT)** for the port. If the average waiting time spikes from 4 hours to 24 hours, it indicates heavy port congestion—invaluable data for logistics companies planning supply chain drop-offs.

---

### 2. Anomaly Detection & Maritime Security

AIS spoofing, illegal fishing, and dark fleet operations are massive global issues. You can write simple mathematical queries to detect suspicious behavior in real time.

* **How it works:** * **Dark Vessels:** If a ship suddenly stops broadcasting points inside your bounding box and reappears 12 hours later far away, flag it as a "Potential AIS Gap Event."
* **Speed Anomalies:** Flag any vessel where the calculated speed between two GPS points exceeds the maximum mechanical speed capability listed for that type of vessel (indicating forged or spoofed location data).
* **Draft Anomalies:** If a ship's static data reports a `maximum_static_draught` of 12 meters, but its stream payload reports a current draught of 15 meters, flag it for data corruption or unsafe overloading.



---

### 3. Predictive ETA (Estimated Time of Arrival) Models

The `Eta` field manually entered into the `ShipStaticData` by the crew is notorious for being inaccurate or outdated. You can build a programmatic ETA engine.

* **How it works:** Calculate the distance ($d$) between the ship's current `latitude`/`longitude` and the destination port coordinates. Divide that distance by its current moving speed over ground (`Sog`).

$$\text{Calculated Travel Time} = \frac{\text{Distance to Port}}{\text{Speed Over Ground}}$$


* **Value:** You can create an automated alerting system (e.g., via a Slack bot or email webhook) that triggers when a specific high-value cargo ship is exactly 2 hours away from the dock, allowing port operators to ready the line handlers and crane crews.

---

### 4. Emissions Tracking & Environmental Analytics

Governments and environmental agencies track port authority compliance by estimating carbon outputs from active ships near coastal waters.

* **How it works:** Match the `ship_type` (e.g., Mega Container Ship vs. Small Tugboat) with standard maritime fuel consumption tables. By continuously running a query that multiplies the ship's active time spent moving inside your bounding box by its `Sog`, you can approximate its engine load and fuel burn rate.
* **Value:** Generates hourly or weekly environmental impact reports detailing estimated $CO_2$ or sulfur emissions produced specifically within municipal port limits.

---

### 5. Berth Optimization & Spatial Hull Matching

When a port scheduler needs to park a ship, they must ensure the physical dock is deep enough and long enough for the vessel.

* **How it works:** Use the `Dimension` block (`A`, `B`, `C`, `D`) from the `ShipStaticData` to calculate the exact length and beam (width) of incoming ships.
* **Value:** Cross-reference the incoming ship dimensions against your available dock sizes in ClickHouse. The system can automatically flag a warning if an incoming vessel is too long or draws too much water (`draught`) to safely fit into its assigned slip.

## Security & Secrets

- Do not check credentials into the repo. Use Kubernetes secrets, external secret stores, or `.env` files excluded from VCS for local dev.

## License
This project is licensed under the MIT License — see `LICENSE` for details.
