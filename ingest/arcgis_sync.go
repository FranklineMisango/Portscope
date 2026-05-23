package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type arcgisResponse struct {
	Features []struct {
		Attributes map[string]interface{} `json:"attributes"`
	} `json:"features"`
}

type geocodeResult struct {
	lat float64
	lon float64
	ok  bool
}

var geocodeCache = struct {
	sync.Mutex
	items map[string]geocodeResult
}{items: map[string]geocodeResult{}}

func parseArcGISDate(attrs map[string]interface{}) (time.Time, bool) {
	if raw, ok := attrs["date"]; ok {
		switch typed := raw.(type) {
		case string:
			if parsed, err := time.Parse("2006-01-02", typed); err == nil {
				return parsed, true
			}
		case float64:
			if typed > 0 {
				return time.Unix(int64(typed)/1000, 0).UTC(), true
			}
		}
	}
	year, yOK := toInt(attrs["year"])
	month, mOK := toInt(attrs["month"])
	day, dOK := toInt(attrs["day"])
	if yOK && mOK && dOK && year > 0 && month > 0 && day > 0 {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
	}
	return time.Time{}, false
}

func toInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func attrString(attrs map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if raw, ok := attrs[key]; ok {
			text := strings.TrimSpace(fmt.Sprintf("%v", raw))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func attrRawJSON(attrs map[string]interface{}) []byte {
	if attrs == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func geocodeLocation(ctx context.Context, name, country string) (float64, float64, bool) {
	cacheKey := strings.ToLower(strings.TrimSpace(name) + "|" + strings.TrimSpace(country))
	geocodeCache.Lock()
	if cached, ok := geocodeCache.items[cacheKey]; ok {
		geocodeCache.Unlock()
		return cached.lat, cached.lon, cached.ok
	}
	geocodeCache.Unlock()

	queryParts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		queryParts = append(queryParts, trimmed)
	}
	if trimmed := strings.TrimSpace(country); trimmed != "" {
		queryParts = append(queryParts, trimmed)
	}
	if len(queryParts) == 0 {
		return 0, 0, false
	}

	u, _ := url.Parse("https://nominatim.openstreetmap.org/search")
	q := u.Query()
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	q.Set("q", strings.Join(queryParts, ", "))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("User-Agent", "Portscope-ArcGIS-Sync/1.0 (+https://github.com)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false
	}

	var items []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil || len(items) == 0 {
		return 0, 0, false
	}
	lat, errLat := strconv.ParseFloat(fmt.Sprintf("%v", items[0]["lat"]), 64)
	lon, errLon := strconv.ParseFloat(fmt.Sprintf("%v", items[0]["lon"]), 64)
	if errLat != nil || errLon != nil {
		return 0, 0, false
	}

	geocodeCache.Lock()
	geocodeCache.items[cacheKey] = geocodeResult{lat: lat, lon: lon, ok: true}
	geocodeCache.Unlock()
	return lat, lon, true
}

func fetchArcGISFeatures(ctx context.Context, endpoint string) ([]map[string]interface{}, error) {
	const pageSize = 1000
	const maxRecords = 10000

	allFeatures := make([]map[string]interface{}, 0)
	offset := 0

	for offset < maxRecords {
		// Build paginated URL
		pageURL := endpoint
		if strings.Contains(endpoint, "?") {
			pageURL += "&resultRecordCount=" + strconv.Itoa(pageSize) + "&resultOffset=" + strconv.Itoa(offset)
		} else {
			pageURL += "?resultRecordCount=" + strconv.Itoa(pageSize) + "&resultOffset=" + strconv.Itoa(offset)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		var out struct {
			Features []struct {
				Attributes map[string]interface{} `json:"attributes"`
			} `json:"features"`
			ExceededTransferLimit bool `json:"exceededTransferLimit"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		features := make([]map[string]interface{}, 0, len(out.Features))
		for _, feature := range out.Features {
			if len(feature.Attributes) == 0 {
				continue
			}
			features = append(features, feature.Attributes)
		}
		allFeatures = append(allFeatures, features...)

		if !out.ExceededTransferLimit || len(features) == 0 {
			break
		}
		offset += pageSize
	}

	return allFeatures, nil
}

func toFloatFromAttrs(attrs map[string]interface{}, key string) (float64, bool) {
	if raw, ok := attrs[key]; ok {
		switch v := raw.(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func upsertPortRecords(ctx context.Context, db *sql.DB, features []map[string]interface{}) error {
	for _, attrs := range features {
		name := attrString(attrs, "fullname", "portname", "name")
		if name == "" {
			continue
		}
		country := attrString(attrs, "country")
		iso3 := attrString(attrs, "ISO3", "iso3")

		// Use lat/lon from the API payload if available (PortWatch_ports_database has them)
		var geom interface{}
		lat, latOk := toFloatFromAttrs(attrs, "lat")
		lon, lonOk := toFloatFromAttrs(attrs, "lon")
		if latOk && lonOk {
			geom = fmt.Sprintf("SRID=4326;POINT(%f %f)", lon, lat)
		} else {
			// fallback to geocoding
			foundLat, foundLon, ok := geocodeLocation(ctx, name, country)
			if ok {
				geom = fmt.Sprintf("SRID=4326;POINT(%f %f)", foundLon, foundLat)
			}
		}

		_, err := db.ExecContext(ctx, `INSERT INTO ports (name, country, iso3, source_payload, geom, last_seen)
			VALUES ($1, $2, $3, $4, ST_GeomFromEWKT($5), now())
			ON CONFLICT (name) DO UPDATE SET
				country = EXCLUDED.country,
				iso3 = EXCLUDED.iso3,
				source_payload = EXCLUDED.source_payload,
				geom = COALESCE(EXCLUDED.geom, ports.geom),
				last_seen = EXCLUDED.last_seen`, name, country, iso3, attrRawJSON(attrs), geom)
		if err != nil {
			return err
		}
	}
	return nil
}

func upsertChokepointRecords(ctx context.Context, db *sql.DB, features []map[string]interface{}) error {
	for _, attrs := range features {
		name := attrString(attrs, "portname", "name")
		if name == "" {
			continue
		}
		sourceDate, _ := parseArcGISDate(attrs)
		lat, lon, ok := geocodeLocation(ctx, name, "")
		var geom interface{}
		if ok {
			geom = fmt.Sprintf("SRID=4326;POINT(%f %f)", lon, lat)
		}
		_, err := db.ExecContext(ctx, `INSERT INTO chokepoints (name, source_date, source_payload, geom, last_seen)
			VALUES ($1, $2, $3, ST_GeomFromEWKT($4), now())
			ON CONFLICT (name) DO UPDATE SET
				source_date = EXCLUDED.source_date,
				source_payload = EXCLUDED.source_payload,
				geom = COALESCE(EXCLUDED.geom, chokepoints.geom),
				last_seen = EXCLUDED.last_seen`, name, nullableDate(sourceDate), attrRawJSON(attrs), geom)
		if err != nil {
			return err
		}
	}
	return nil
}

func nullableDate(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func syncArcGISFeeds(ctx context.Context, pgDsn string) error {
	db, err := sql.Open("pgx", pgDsn)
	if err != nil {
		return fmt.Errorf("postgres open: %w", err)
	}
	defer db.Close()

	portsEndpoint := os.Getenv("ARCGIS_PORTS_URL")
	if portsEndpoint == "" {
		portsEndpoint = "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/PortWatch_ports_database/FeatureServer/0/query?where=1%3D1&outFields=*&outSR=4326&f=json"
	}
	chokepointsEndpoint := os.Getenv("ARCGIS_CHOKEPOINTS_URL")
	if chokepointsEndpoint == "" {
		chokepointsEndpoint = "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/PortWatch_chokepoints_database/FeatureServer/0/query?where=1%3D1&outFields=*&outSR=4326&f=json"
	}

	ports, err := fetchArcGISFeatures(ctx, portsEndpoint)
	if err != nil {
		return fmt.Errorf("fetch ports: %w", err)
	}
	if err := upsertPortRecords(ctx, db, ports); err != nil {
		return fmt.Errorf("upsert ports: %w", err)
	}

	chokepoints, err := fetchArcGISFeatures(ctx, chokepointsEndpoint)
	if err != nil {
		return fmt.Errorf("fetch chokepoints: %w", err)
	}
	if err := upsertChokepointRecords(ctx, db, chokepoints); err != nil {
		return fmt.Errorf("upsert chokepoints: %w", err)
	}

	log.Printf("synced %d ports and %d chokepoints from ArcGIS", len(ports), len(chokepoints))
	return nil
}

func runArcGISSyncLoop(ctx context.Context, pgDsn string) error {
	interval := 6 * time.Hour
	if raw := os.Getenv("ARCGIS_SYNC_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		}
	}

	for {
		if err := syncArcGISFeeds(ctx, pgDsn); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}
