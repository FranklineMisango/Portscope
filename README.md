

# Portscope

Interactive exploration of worldwide port activity and chokepoint intensity using local PortWatch datasets.

[![Build Status](https://img.shields.io/badge/build-passing-brightgreen.svg)](https://github.com/yourorg/portscope)
[![System Status](https://img.shields.io/badge/system-stable-green.svg)](https://github.com/yourorg/portscope)
Issues [(github.com in Bing)](https://www.bing.com/search?q="https%3A%2F%2Fgithub.com%2Fyourorg%2Fportscope%2Fissues")
Version [(github.com in Bing)](https://www.bing.com/search?q="https%3A%2F%2Fgithub.com%2Fyourorg%2Fportscope%2Freleases")

---

## Tech Stack

https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white
`https://img.shields.io/badge/PostgreSQL-336791?style=for-the-badge&logo=postgresql&logoColor=white`
`https://img.shields.io/badge/PostGIS-006400?style=for-the-badge&logo=postgis&logoColor=white`
`https://img.shields.io/badge/React-20232A?style=for-the-badge&logo=react&logoColor=61DAFB`
`https://img.shields.io/badge/Leaflet-199900?style=for-the-badge&logo=leaflet&logoColor=white`
`https://img.shields.io/badge/Docker-2496ED?style=for-the-badge&logo=docker&logoColor=white`
`https://img.shields.io/badge/Kubernetes-326CE5?style=for-the-badge&logo=kubernetes&logoColor=white`
`https://img.shields.io/badge/Python-3776AB?style=for-the-badge&logo=python&logoColor=white`

---

## Features

### Core Capabilities
- **Interactive Map**: Explore ports and chokepoints on a dark, IMF-style map.
- **Local PortWatch Data**: Ports and chokepoints load from checked-in GeoJSON files.
- **Click-through Metrics**: Select a marker to see the PortWatch page ID and metrics.
- **Containerized Deployment**: Dockerized microservices with Kubernetes scaling.

### Advanced Analytics
- **Traffic Heatmaps**: Visualize density around major ports.
- **Cargo Composition**: Breakdown by vessel type and cargo class.
- **Seasonal Trends**: Compare traffic year-over-year.
- **Forecast Alerts**: Predict surges or bottlenecks.

---

## Architecture

```text
[Frontend: React + Leaflet/Mapbox]
        |
        v
[Go API Gateway] ----> [Redis Cache]
        |
        v
[PostgreSQL + PostGIS] <---- [ArcGIS Daily Feeds]
        |
        v
[ML Microservice: Python/ML.NET]
        |
        v
[Monitoring: Prometheus + Grafana]
```

---

## Database Schema

| Table            | Purpose                  | Key Fields |
|------------------|--------------------------|------------|
| **ports**        | Port metadata + polygons | id, name, country, iso3, observed_on, metrics, geom |
| **chokepoints**  | Daily chokepoint data    | id, name, observed_on, metrics, geom |
| **ships**        | Vessel registry          | imo, mmsi, type, flag, tonnage |
| **traffic_logs** | Arrivals/departures      | port_id, ship_id, timestamp, event_type |
| **ais_messages** | Raw AIS feed             | mmsi, lat, lon, speed, heading, timestamp |

Indexes:
- GIN on `geom` for geospatial queries
- BTREE on `timestamp` for time‑range queries

---

## API Endpoints

- `GET /ports` → Daily ports feed
- `GET /chokepoints` → Daily chokepoints feed
- `GET /port/{id}/traffic?range=30d` → Historical traffic
- `GET /port/{id}/forecast` → Predictive arrivals/departures

---

## Quick Start

### Clone the repo
```bash
git clone https://github.com/yourorg/portscope.git
cd portscope
```

### Configure environment
```env
POSTGRES_USER=postgres
POSTGRES_PASSWORD=postgres
POSTGRES_DB=portscope
DATA_REFRESH_INTERVAL=6h
```

### Run with Docker
```bash
docker compose -f ingest/docker-compose.yml up --build --force-recreate
```

### Access the app
- Frontend: `http://localhost:3000`
- API: `http://localhost:8080`
- Grafana: `http://localhost:3001`

### If you still see the old AIS UI
```bash
docker compose -f ingest/docker-compose.yml down -v --remove-orphans
docker compose -f ingest/docker-compose.yml up --build --force-recreate
```

If you hit `relation "ports" does not exist`, the DB volume already existed before the migrations mount was added. Use the `down -v` command above once to recreate it.

---

## Example Workflow

1. Open the map → see global ports.
2. Click a port or chokepoint → inspect daily metrics.
3. View the map and compare port activity across regions.
4. Refresh the ArcGIS feeds on the schedule you set.

---

## Roadmap

- [x] ArcGIS daily feed ingestion
- [x] PostGIS schema for ports and ships
- [x] Interactive map frontend
- [ ] Advanced ML forecasting
- [ ] Cloud deployment (Azure/Kubernetes)
- [ ] Multi‑asset expansion (cargo, fishing, passenger)

---

## License
MIT License
---