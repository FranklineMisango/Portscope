# AIS Stream Tests — ClickHouse + Grafana

This project streams live AIS vessel data into **ClickHouse** and visualizes it in **Grafana**.

## Setup

### 1. Environment variables

Create a `.env` file in this directory (next to `docker-compose.yml`):

```env
# ClickHouse
CLICKHOUSE_USER=Arithmax_research
CLICKHOUSE_PASSWORD=your_clickhouse_password

# AIS stream API key
AISAPIKEY=your_aisstream_api_key_here

# Grafana
GF_SECURITY_ADMIN_USER=admin
GF_SECURITY_ADMIN_PASSWORD=admin
```


### 2. Start ClickHouse & Grafana

```bash
docker compose up -d
```

### 3. Create the database in ClickHouse

```bash
# Load env vars and create the database
set -a; source .env; set +a
curl -u "$CLICKHOUSE_USER:$CLICKHOUSE_PASSWORD" \
  'http://localhost:8123/' \
  --data-binary "CREATE DATABASE IF NOT EXISTS vessels_tracking"
```

### 4. Connect Grafana to ClickHouse

1. Open Grafana at **http://localhost:3000** (login: `admin` / `admin`)
2. Go to **Connections → Data Sources → Add data source**
3. Search for **ClickHouse** and select it
4. Fill in:

| Field | Value |
|---|---|
| **Server address** | `clickhouse` |
| **Server port** | `8123` |
| **Protocol** | `HTTP` |
| **Secure Connection** | Off |
| **Username** | `Arithmax_research` |
| **Password** | `_t3aeV6eL!sb$-J` |
| **Default database** | `vessels_tracking` |

> **Important:** Use `clickhouse` as the server address (the Docker service name), **not** `localhost`. Grafana and ClickHouse run in separate containers and communicate over the internal Docker network.

### 5. Run the AIS stream ingester

```bash
python main.py
```

This connects to the AIS stream via WebSocket and inserts position reports into the `vessels_tracking.ais_data` table in ClickHouse in real time.

### 6. Query the data in Grafana Explore

Once the ingester is running, you can query the data in Grafana **Explore**:

```sql
SELECT *
FROM vessels_tracking.ais_data
ORDER BY ts DESC
LIMIT 100
```

Or visualize vessel positions on a map using a **Geomap** panel.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Authentication failed` | Wrong ClickHouse credentials | Check `.env` values match docker-compose |
| `connection refused` | Grafana using `localhost` instead of `clickhouse` | Use Docker service name `clickhouse` as server address |
| No data in tables | Ingester not running | Run `python main.py` |

## Project structure

```
ais_stream_tests/
├── .env                  # Secrets (gitignored)
├── docker-compose.yml    # ClickHouse + Grafana services
├── main.py               # AIS stream ingester → ClickHouse
└── README.md             # This file
```
