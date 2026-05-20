Grafana is provisioned to use Prometheus at `http://prometheus:9090`.

Start services:

```bash
cd ingest
docker compose up -d
```

Grafana UI: http://localhost:3001 (admin/admin)

Notes:
- You can place dashboard JSON files under `monitoring/grafana/dashboards/` and they will be picked up by Grafana provisioning.
