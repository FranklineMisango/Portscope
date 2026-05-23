"""
PortWatch Data Normalizer
=========================
Transforms raw scraped PortWatch data into structured metrics suitable for:
  - Database storage (normalized tables)
  - Chart/visualization consumption
  - API response formatting

Handles:
  - Vessel count time series
  - Industry breakdowns
  - CSV dataset flattening
  - Metric aggregation
"""

import json
import logging
from datetime import datetime, timedelta
from typing import Dict, Any, List, Optional, Tuple
from collections import defaultdict

log = logging.getLogger('portwatch_normalizer')


class PortWatchNormalizer:
    """Normalizes raw scraped PortWatch data into structured metric sets."""

    @staticmethod
    def normalize_vessel_counts(raw_counts: Dict[str, int]) -> Dict[str, Any]:
        """
        Normalize vessel count data into a consistent schema.
        
        Input:  {"total": 19787, "container": 5513, "dry_bulk": 5222, ...}
        Output: {
          "total_vessels": 19787,
          "vessel_breakdown": {
            "container": {"count": 5513, "share_pct": 27.86},
            "dry_bulk": {"count": 5222, "share_pct": 26.39},
            ...
          },
          "primary_type": "tanker",  # most common vessel type
          "diversity_index": 0.78    # 0-1, how diversified the traffic is
        }
        """
        total = raw_counts.get("total", 0)
        if total == 0:
            # Try to sum all category counts
            category_keys = ["container", "dry_bulk", "general_cargo", "roro", "tanker"]
            total = sum(raw_counts.get(k, 0) for k in category_keys)
        
        breakdown = {}
        vessel_types = {
            "container": "Container",
            "dry_bulk": "Dry Bulk",
            "general_cargo": "General Cargo",
            "roro": "RoRo",
            "tanker": "Tanker",
        }
        
        primary_type = None
        max_count = 0
        
        for key, label in vessel_types.items():
            count = raw_counts.get(key, 0)
            if count > 0:
                share = round((count / total * 100), 1) if total > 0 else 0
                breakdown[key] = {
                    "label": label,
                    "count": count,
                    "share_pct": share,
                }
                if count > max_count:
                    max_count = count
                    primary_type = label
        
        # Diversity index (1 - sum of squared shares)
        diversity = 0.0
        if total > 0 and len(breakdown) > 1:
            sum_squares = sum(
                (v["count"] / total) ** 2 for v in breakdown.values()
            )
            diversity = round(1 - sum_squares, 4)
        
        return {
            "total_vessels": total,
            "vessel_breakdown": breakdown,
            "primary_type": primary_type or "unknown",
            "diversity_index": diversity,
            "raw": raw_counts,
        }

    @staticmethod
    def normalize_csv_timeseries(
        csv_rows: List[Dict[str, Any]],
        date_field: str = "date",
        value_field: str = "count",
        label_field: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Normalize CSV dataset rows into a time series format.
        
        Input:  [{"date": "2024-01", "count": "1234"}, ...]
        Output: {
          "labels": ["2024-01", "2024-02", ...],
          "values": [1234, 1456, ...],
          "series": [...],
          "summary": {
            "min": 1234,
            "max": 5678,
            "avg": 2345,
            "total": ...
          }
        }
        """
        if not csv_rows:
            return {"labels": [], "values": [], "series": [], "summary": {}}
        
        labels = []
        values = []
        series_data = defaultdict(list)
        
        for row in csv_rows:
            date_val = row.get(date_field, "")
            raw_val = row.get(value_field, "0")
            
            try:
                val = float(raw_val.replace(",", "")) if isinstance(raw_val, str) else float(raw_val)
            except (ValueError, TypeError):
                val = 0.0
            
            labels.append(str(date_val))
            values.append(val)
            
            if label_field:
                group = row.get(label_field, "default")
                series_data[group].append({"date": date_val, "value": val})
        
        # Compute summary
        if values:
            summary = {
                "min": min(values),
                "max": max(values),
                "avg": round(sum(values) / len(values), 1),
                "total": sum(values),
                "latest": values[-1] if values else 0,
                "trend": "up" if len(values) > 1 and values[-1] > values[0] else "down" if len(values) > 1 else "stable",
            }
        else:
            summary = {}
        
        # Build series for multi-line charts
        series = []
        if label_field:
            for group_name, points in series_data.items():
                series.append({
                    "name": group_name,
                    "data": [p["value"] for p in points],
                })
        
        return {
            "labels": labels,
            "values": values,
            "series": series,
            "summary": summary,
        }

    @staticmethod
    def normalize_industries(
        raw_industries: List[Dict[str, Any]],
        vessel_breakdown: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """
        Normalize industry data.
        
        Input:  [{"industry": "Mineral Products"}, ...]
        Output: {
          "top_industries": ["Mineral Products", ...],
          "industry_breakdown": [...],
          "primary_industry": "Mineral Products"
        }
        """
        industries = []
        for item in raw_industries:
            name = item.get("industry", item.get("name", ""))
            share = item.get("share", item.get("percentage", None))
            if name:
                entry = {"name": name}
                if share is not None:
                    entry["share_pct"] = float(share)
                industries.append(entry)
        
        return {
            "top_industries": [i["name"] for i in industries],
            "industry_breakdown": industries,
            "primary_industry": industries[0]["name"] if industries else None,
        }

    @staticmethod
    def build_metric_set(
        page_data: Dict[str, Any],
        include_raw: bool = False,
    ) -> Dict[str, Any]:
        """
        Build a complete, normalized metric set from raw page data.
        This is the primary API that the frontend will query.
        
        Returns a dict suitable for:
          - Direct API JSON response
          - Visualization rendering
          - Comparison across ports
        """
        normalized = {
            "pageid": page_data.get("pageid", ""),
            "port_name": page_data.get("port_name", ""),
            "port_type": page_data.get("port_type", "port"),
            "country": page_data.get("port_country", ""),
            "coordinates": page_data.get("coordinates"),
            "scraped_at": page_data.get("scraped_at", ""),
        }
        
        # Vessel metrics
        vessel_data = PortWatchNormalizer.normalize_vessel_counts(
            page_data.get("vessel_counts", {})
        )
        normalized["vessels"] = vessel_data
        
        # Industry metrics
        industry_data = PortWatchNormalizer.normalize_industries(
            page_data.get("top_industries", []),
            vessel_data.get("vessel_breakdown"),
        )
        normalized["industries"] = industry_data
        
        # Time series from CSV datasets
        csv_datasets = page_data.get("csv_datasets", {})
        timeseries = {}
        for dataset_name, rows in csv_datasets.items():
            timeseries[dataset_name] = PortWatchNormalizer.normalize_csv_timeseries(rows)
        normalized["timeseries"] = timeseries
        
        # Flatten additional metrics
        additional = page_data.get("additional_metrics", {})
        if additional:
            normalized["additional"] = {
                k: v for k, v in additional.items()
                if not isinstance(v, (dict, list)) or len(str(v)) < 500
            }
        
        # Compute aggregate metrics
        normalized["aggregates"] = PortWatchNormalizer.compute_aggregates(normalized)
        
        if include_raw:
            normalized["raw"] = page_data
        
        return normalized

    @staticmethod
    def compute_aggregates(normalized: Dict[str, Any]) -> Dict[str, Any]:
        """Compute aggregate/summary metrics from normalized data."""
        aggregates = {}
        
        vessels = normalized.get("vessels", {})
        aggregates["total_vessels"] = vessels.get("total_vessels", 0)
        aggregates["primary_vessel_type"] = vessels.get("primary_type", "unknown")
        aggregates["vessel_diversity"] = vessels.get("diversity_index", 0)
        
        industries = normalized.get("industries", {})
        aggregates["primary_industry"] = industries.get("primary_industry", "unknown")
        
        # Traffic change/trend from time series
        for name, ts in normalized.get("timeseries", {}).items():
            summary = ts.get("summary", {})
            if "trend" in summary:
                aggregates[f"{name}_trend"] = summary["trend"]
                aggregates[f"{name}_latest"] = summary.get("latest", 0)
                aggregates[f"{name}_avg"] = summary.get("avg", 0)
        
        return aggregates


# =============================================================================
# API Response Builders
# =============================================================================

class MetricResponseBuilder:
    """Builds API responses for the sidebar visualization."""
    
    @staticmethod
    def build_sidebar_metrics(
        metric_sets: List[Dict[str, Any]],
        selected_metrics: Optional[List[str]] = None,
    ) -> Dict[str, Any]:
        """
        Build the response that the sidebar consumes.
        
        If selected_metrics is None, returns all available metric options.
        Otherwise, returns only the requested metrics.
        """
        if not metric_sets:
            return {
                "available_metrics": MetricResponseBuilder.get_available_metrics(),
                "selected_data": {},
            }
        
        # Merge across multiple pages if needed
        merged = {}
        for ms in metric_sets:
            for key in ["vessels", "industries", "timeseries", "aggregates"]:
                if key in ms:
                    if key not in merged:
                        merged[key] = ms[key]
                    elif isinstance(ms[key], dict):
                        merged[key].update(ms[key])
        
        if selected_metrics:
            filtered = {}
            for metric in selected_metrics:
                if metric in merged:
                    filtered[metric] = merged[metric]
                elif metric in merged.get("aggregates", {}):
                    if "aggregates" not in filtered:
                        filtered["aggregates"] = {}
                    filtered["aggregates"][metric] = merged["aggregates"][metric]
                elif metric in merged.get("timeseries", {}):
                    if "timeseries" not in filtered:
                        filtered["timeseries"] = {}
                    filtered["timeseries"][metric] = merged["timeseries"][metric]
            merged = filtered
        
        return {
            "available_metrics": MetricResponseBuilder.get_available_metrics(),
            "selected_data": merged,
        }
    
    @staticmethod
    def get_available_metrics() -> List[Dict[str, Any]]:
        """Return all available metric types the sidebar can display."""
        return [
            {
                "id": "total_vessels",
                "label": "Total Vessels",
                "category": "vessels",
                "type": "number",
                "chart_type": "stat",
            },
            {
                "id": "vessel_breakdown",
                "label": "Vessel Type Breakdown",
                "category": "vessels",
                "type": "breakdown",
                "chart_type": "pie",
                "sub_metrics": [
                    {"id": "container", "label": "Container"},
                    {"id": "dry_bulk", "label": "Dry Bulk"},
                    {"id": "general_cargo", "label": "General Cargo"},
                    {"id": "roro", "label": "RoRo"},
                    {"id": "tanker", "label": "Tanker"},
                ],
            },
            {
                "id": "vessel_diversity",
                "label": "Traffic Diversity",
                "category": "vessels",
                "type": "number",
                "chart_type": "gauge",
            },
            {
                "id": "primary_vessel_type",
                "label": "Primary Vessel Type",
                "category": "vessels",
                "type": "label",
                "chart_type": "stat",
            },
            {
                "id": "primary_industry",
                "label": "Primary Industry",
                "category": "industries",
                "type": "label",
                "chart_type": "stat",
            },
            {
                "id": "top_industries",
                "label": "Top Industries",
                "category": "industries",
                "type": "list",
                "chart_type": "bar",
            },
            {
                "id": "vessel_traffic_monthly",
                "label": "Vessel Traffic (Monthly)",
                "category": "timeseries",
                "type": "timeseries",
                "chart_type": "line",
            },
            {
                "id": "trade_volume",
                "label": "Trade Volume",
                "category": "timeseries",
                "type": "timeseries",
                "chart_type": "line",
            },
        ]
    
    @staticmethod
    def build_chart_config(
        metric_id: str,
        data: Any,
    ) -> Dict[str, Any]:
        """
        Build a chart configuration object for the frontend.
        
        Returns a config that the frontend chart library can consume
        without additional transformation.
        """
        configs = {
            "total_vessels": lambda d: {
                "type": "stat",
                "title": "Total Vessels",
                "value": d,
                "format": "number",
            },
            "vessel_breakdown": lambda d: {
                "type": "pie",
                "title": "Vessel Type Breakdown",
                "data": [
                    {"label": v["label"], "value": v["count"], "share": v["share_pct"]}
                    for v in d.values()
                ],
                "colors": ["#63d6ff", "#8a7dff", "#76e4b5", "#ffcc66", "#ff8a5b"],
            },
            "vessel_traffic_monthly": lambda d: {
                "type": "line",
                "title": "Monthly Vessel Traffic",
                "labels": d.get("labels", []),
                "values": d.get("values", []),
                "series": d.get("series", []),
                "summary": d.get("summary", {}),
                "fill": True,
                "fillColor": "rgba(99, 214, 255, 0.1)",
                "strokeColor": "#63d6ff",
            },
            "top_industries": lambda d: {
                "type": "bar",
                "title": "Top Industries",
                "labels": [i["name"] for i in d],
                "values": [i.get("share_pct", 100 / max(len(d), 1)) for i in d],
                "colors": ["#63d6ff", "#8a7dff", "#76e4b5"],
            },
        }
        
        builder = configs.get(metric_id)
        if builder:
            return builder(data)
        
        # Generic fallback
        return {
            "type": "stat",
            "title": metric_id,
            "value": data,
        }


# =============================================================================
# Test / Demo
# =============================================================================

def demo_normalization():
    """Run a quick demo of the normalizer."""
    sample_data = {
        "pageid": "c57c79bf612b4372b08a9c6ea9c97ef0",
        "port_name": "Suez Canal",
        "port_type": "chokepoint",
        "port_country": "Egypt",
        "coordinates": [30.593, 32.437],
        "vessel_counts": {
            "total": 19787,
            "container": 5513,
            "dry_bulk": 5222,
            "general_cargo": 1868,
            "roro": 766,
            "tanker": 6418,
        },
        "top_industries": [
            {"industry": "Mineral Products"},
            {"industry": "Vegetable Products"},
            {"industry": "Chemical & Allied Industries"},
        ],
        "csv_datasets": {
            "vessel_traffic_2024": [
                {"date": "2024-01", "count": "1500"},
                {"date": "2024-02", "count": "1620"},
                {"date": "2024-03", "count": "1580"},
            ],
        },
        "scraped_at": "2024-01-01T00:00:00",
    }
    
    normalized = PortWatchNormalizer.build_metric_set(sample_data, include_raw=True)
    print(json.dumps(normalized, indent=2))
    return normalized


if __name__ == "__main__":
    demo_normalization()
