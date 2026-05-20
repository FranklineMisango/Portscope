# Portscope

Portscope is a maritime analytics platform for exploring global port activity, tracking live vessel movement, and forecasting traffic trends.

## Features
- Interactive world map (Leaflet/Mapbox) with clickable ports
- Live AIS ship tracking via [aisstream.io](https://aisstream.io/)
- Historical traffic queries (30 days, 1 year, 2 years)
- Predictive ML pipeline for arrivals/departures
- Geospatial queries powered by PostGIS
- Dockerized microservices for easy deployment

## Tech Stack
- **Backend**: Go (REST API, AIS ingestion)
- **Database**: PostgreSQL + PostGIS
- **Streaming**: Kafka / Redis Streams
- **ML Pipeline**: Python (scikit-learn/PyTorch) or ML.NET
- **Frontend**: React + Leaflet.js / Mapbox GL JS
- **Deployment**: Docker + Kubernetes
- **Monitoring**: Prometheus + Grafana
