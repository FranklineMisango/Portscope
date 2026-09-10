package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	cache "github.com/patrickmn/go-cache"
)

// c is the shared in-memory cache, initialized in main.go
var c *cache.Cache

// ──── PortWatch Data Types ────

// PortWatchPageResult is the full response for a single pageid lookup.
type PortWatchPageResult struct {
	PageID      string               `json:"pageid"`
	PortID      string               `json:"port_id"`
	Name        string               `json:"name"`
	Country     string               `json:"country,omitempty"`
	ISO3        string               `json:"iso3,omitempty"`
	Lat         float64              `json:"lat"`
	Lon         float64              `json:"lon"`
	PortType    string               `json:"port_type"` // "port" or "chokepoint"
	Metrics     *PortWatchMetrics    `json:"metrics,omitempty"`
	TimeSeries  *PortWatchTimeSeries `json:"timeseries,omitempty"`
	ExternalURL string               `json:"external_url"`
	// Data we could not scrape but IMF PortWatch provides
	UnavailableData []UnavailableDataItem `json:"unavailable_data,omitempty"`
}

type UnavailableDataItem struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	ExternalURL string `json:"external_url"`
}

// PortWatchMetrics holds the aggregated metrics for a port/chokepoint.
type PortWatchMetrics struct {
	TotalVessels    int               `json:"total_vessels"`
	PrimaryType     string            `json:"primary_vessel_type"`
	DiversityIndex  float64           `json:"diversity_index"`
	VesselBreakdown []VesselBreakdown `json:"vessel_breakdown"`
	TopIndustries   []string          `json:"top_industries"`
	PrimaryIndustry string            `json:"primary_industry"`
	CountryShare    *CountryShare     `json:"country_share,omitempty"`
	// Daily aggregates
	TotalPortcalls    int     `json:"total_portcalls"`
	TotalImports      float64 `json:"total_imports"`
	TotalExports      float64 `json:"total_exports"`
	AvgDailyPortcalls float64 `json:"avg_daily_portcalls"`
	DataRangeStart    string  `json:"data_range_start"`
	DataRangeEnd      string  `json:"data_range_end"`
}

type VesselBreakdown struct {
	Label    string  `json:"label"`
	Count    int     `json:"count"`
	SharePct float64 `json:"share_pct"`
}

type CountryShare struct {
	ImportShare float64 `json:"import_share_pct"`
	ExportShare float64 `json:"export_share_pct"`
}

type PortWatchTimeSeries struct {
	Portcalls []TimeSeriesPoint `json:"portcalls,omitempty"`
	Imports   []TimeSeriesPoint `json:"imports,omitempty"`
	Exports   []TimeSeriesPoint `json:"exports,omitempty"`
}

type TimeSeriesPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// ──── ArcGIS Feature Service Client ────

var (
	portwatchClient     *PortWatchFSClient
	portwatchClientOnce sync.Once
)

type PortWatchFSClient struct {
	httpClient   *http.Client
	basePortURL  string
	baseChokeURL string
	cache        map[string]*cacheEntry
	cacheMu      sync.RWMutex
}

type cacheEntry struct {
	data      interface{}
	expiresAt time.Time
}

const portwatchCacheTTL = 10 * time.Minute

func getPortWatchClient() *PortWatchFSClient {
	portwatchClientOnce.Do(func() {
		portwatchClient = &PortWatchFSClient{
			httpClient:   &http.Client{Timeout: 60 * time.Second},
			basePortURL:  os.Getenv("PW_PORTS_FS_URL"),
			baseChokeURL: os.Getenv("PW_CHOKEPOINTS_FS_URL"),
			cache:        make(map[string]*cacheEntry),
		}
		if portwatchClient.basePortURL == "" {
			portwatchClient.basePortURL = "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/Daily_Ports_Data/FeatureServer/0/query"
		}
		if portwatchClient.baseChokeURL == "" {
			portwatchClient.baseChokeURL = "https://services9.arcgis.com/weJ1QsnbMYJlCHdG/arcgis/rest/services/Daily_Chokepoints_Data/FeatureServer/0/query"
		}
	})
	return portwatchClient
}

func (c *PortWatchFSClient) getCached(key string) (interface{}, bool) {
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	entry, ok := c.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.data, true
}

func (c *PortWatchFSClient) setCache(key string, data interface{}) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.cache[key] = &cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(portwatchCacheTTL),
	}
}

func (c *PortWatchFSClient) queryFS(serviceURL, where, outFields, order string, limit, offset int) ([]map[string]interface{}, bool, error) {
	cacheKey := fmt.Sprintf("fs:%s:%s:%s:%d:%d", serviceURL, where, order, limit, offset)
	if cached, ok := c.getCached(cacheKey); ok {
		data := cached.([]map[string]interface{})
		// Return cached but mark exceeded=false so it doesn't continue paginating
		return data, false, nil
	}

	req, _ := http.NewRequest("GET", serviceURL, nil)
	q := req.URL.Query()
	q.Set("where", where)
	q.Set("outFields", outFields)
	q.Set("orderByFields", order)
	q.Set("returnGeometry", "false")
	q.Set("resultRecordCount", strconv.Itoa(limit))
	q.Set("resultOffset", strconv.Itoa(offset))
	q.Set("f", "json")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Features              []map[string]interface{} `json:"features"`
		ExceededTransferLimit bool                     `json:"exceededTransferLimit"`
		Error                 *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, fmt.Errorf("decode failed: %w", err)
	}
	if result.Error != nil {
		return nil, false, fmt.Errorf("ArcGIS error %d: %s", result.Error.Code, result.Error.Message)
	}

	features := result.Features
	if features == nil {
		features = []map[string]interface{}{}
	}

	c.setCache(cacheKey, features)
	return features, result.ExceededTransferLimit, nil
}

func (c *PortWatchFSClient) fetchAll(serviceURL, where, outFields, order string) ([]map[string]interface{}, error) {
	const pageSize = 1000
	const maxRecords = 50000

	var all []map[string]interface{}
	offset := 0

	for offset < maxRecords {
		features, exceeded, err := c.queryFS(serviceURL, where, outFields, order, pageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, features...)
		if !exceeded || len(features) == 0 {
			break
		}
		offset += pageSize
	}
	return all, nil
}

// ──── PortWatch Data Lookup (uses DB portwatch_scraped_pages or ports table) ────

// pageIDToPortID is loaded from the CSV at startup: pageid -> {portid, fullname, country, iso3, lat, lon}
var pageIDMap map[string]portWatchPortInfo

type portWatchPortInfo struct {
	PortID   string  `json:"portid"`
	FullName string  `json:"fullname"`
	Country  string  `json:"country"`
	ISO3     string  `json:"iso3"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	PortType string  `json:"port_type"`
}

func init() {
	pageIDMap = make(map[string]portWatchPortInfo)
}

// loadPortWatchMapping reads the CSV/GeoJSON port mapping at startup.
// Called once from main.go after DB connection is ready.
func loadPortWatchMapping(db *sql.DB) {
	rows, err := db.Query(`
		SELECT source_payload->>'pageid', source_payload->>'portid', 
		       source_payload->>'fullname', COALESCE(source_payload->>'country', ''),
		       COALESCE(source_payload->>'iso3', source_payload->>'ISO3', ''),
		       'port', ST_Y(geom), ST_X(geom)
		FROM ports WHERE source_payload->>'pageid' IS NOT NULL
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var info portWatchPortInfo
			var pageID string
			if err := rows.Scan(&pageID, &info.PortID, &info.FullName, &info.Country, &info.ISO3, &info.PortType, &info.Lat, &info.Lon); err == nil {
				pageIDMap[pageID] = info
			}
		}
	}

	// Also load chokepoints
	rows2, err := db.Query(`
		SELECT source_payload->>'pageid', source_payload->>'portid', 
		       source_payload->>'fullname', COALESCE(source_payload->>'country', ''),
		       COALESCE(source_payload->>'iso3', source_payload->>'ISO3', ''),
		       'chokepoint', ST_Y(geom), ST_X(geom)
		FROM chokepoints WHERE source_payload->>'pageid' IS NOT NULL
	`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var info portWatchPortInfo
			var pageID string
			if err := rows2.Scan(&pageID, &info.PortID, &info.FullName, &info.Country, &info.ISO3, &info.PortType, &info.Lat, &info.Lon); err == nil {
				pageIDMap[pageID] = info
			}
		}
	}

	log.Printf("[portwatch] loaded %d pageID mappings from DB", len(pageIDMap))
}

func lookupPortByPageID(db *sql.DB, pageID string) (*PortWatchPageResult, error) {
	var name, country, iso3, portType string
	var lat, lon float64
	var portID string

	// First check our in-memory map (covers ALL ports from DB)
	if info, ok := pageIDMap[pageID]; ok {
		portID = info.PortID
		name = info.FullName
		country = info.Country
		iso3 = info.ISO3
		portType = info.PortType
		lat = info.Lat
		lon = info.Lon
	} else {
		// Fall back to direct DB query
		err := db.QueryRow(`
			SELECT source_payload->>'portname', source_payload->>'country', 
			       COALESCE(source_payload->>'iso3', source_payload->>'ISO3', ''),
			       'port', source_payload->>'portid'
			FROM ports WHERE source_payload->>'pageid' = $1
		`, pageID).Scan(&name, &country, &iso3, &portType, &portID)

		if err != nil {
			// Try chokepoints table
			err = db.QueryRow(`
				SELECT source_payload->>'portname', COALESCE(source_payload->>'country', ''),
				       COALESCE(source_payload->>'iso3', source_payload->>'ISO3', ''),
				       'chokepoint', source_payload->>'portid'
				FROM chokepoints WHERE source_payload->>'pageid' = $1
			`, pageID).Scan(&name, &country, &iso3, &portType, &portID)
		}

		if err != nil {
			// If all lookups fail, return a minimal response with external link
			return &PortWatchPageResult{
				PageID:          pageID,
				PortType:        "unknown",
				ExternalURL:     fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
				UnavailableData: getDefaultUnavailableData(pageID),
			}, nil
		}

		// Get coordinates
		if portType == "port" {
			db.QueryRow(`SELECT ST_Y(geom), ST_X(geom) FROM ports WHERE source_payload->>'pageid' = $1`, pageID).Scan(&lat, &lon)
		} else {
			db.QueryRow(`SELECT ST_Y(geom), ST_X(geom) FROM chokepoints WHERE source_payload->>'pageid' = $1`, pageID).Scan(&lat, &lon)
		}
	}

	// If we still don't have a portID, this port has no PortWatch data
	if portID == "" {
		return &PortWatchPageResult{
			PageID:          pageID,
			Name:            name,
			Country:         country,
			PortType:        portType,
			ExternalURL:     fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
			UnavailableData: getDefaultUnavailableData(pageID),
		}, nil
	}

	result := &PortWatchPageResult{
		PageID:          pageID,
		PortID:          portID,
		Name:            name,
		Country:         country,
		ISO3:            iso3,
		Lat:             lat,
		Lon:             lon,
		PortType:        portType,
		ExternalURL:     fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
		UnavailableData: getDefaultUnavailableData(pageID),
	}

	return result, nil
}

func getDefaultUnavailableData(pageID string) []UnavailableDataItem {
	return []UnavailableDataItem{
		{
			Label:       "Top 5 Port Connections",
			Description: "Upstream & downstream port connections derived from AIS vessel trajectory analysis showing where ships come from and go to after visiting this port.",
			ExternalURL: fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
		},
		{
			Label:       "Annual Vessel Composition",
			Description: "Yearly breakdown of vessel types calling at this port (container, tanker, dry bulk, RoRo, general cargo) as % of total vessels.",
			ExternalURL: fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
		},
		{
			Label:       "Spillover Simulator",
			Description: "Interactive tool to simulate ripple effects of disruptions through the regional/global transport network.",
			ExternalURL: fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
		},
		{
			Label:       "Climate Scenarios",
			Description: "Climate risk stress tests showing potential economic losses from more frequent and intense climate extremes.",
			ExternalURL: fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
		},
	}
}

// ──── Fetch PortWatch Data from Local DB (with ArcGIS fallback) ────

func fetchPortWatchData(db *sql.DB, pageID string) (*PortWatchPageResult, error) {
	// 1. Lookup the port from DB
	result, err := lookupPortByPageID(db, pageID)
	if err != nil {
		return nil, fmt.Errorf("lookup failed: %w", err)
	}

	// 2. Try local time-series data first (from ingest pipeline)
	if data, ok := fetchPortWatchFromLocalDB(db, pageID, result); ok {
		return data, nil
	}

	// 3. Fallback: ArcGIS Feature Service (old path, for backwards compatibility)
	//    Remove once ingest pipeline is fully populating portwatch_timeseries.
	return fetchPortWatchFromArcGIS(result)
}

// fetchPortWatchFromLocalDB reads normalized metrics + timeseries from the local DB.
// Returns (result, true) on success, or (nil, false) if no local data exists.
func fetchPortWatchFromLocalDB(db *sql.DB, pageID string, result *PortWatchPageResult) (*PortWatchPageResult, bool) {
	// Try portwatch_normalized_metrics first
	var totalPC int
	var totalImport, totalExport float64
	var dateMin, dateMax string
	var tsData json.RawMessage

	err := db.QueryRow(`
		SELECT COALESCE(aggregates->>'total_portcalls', '0')::int,
		       COALESCE(aggregates->>'total_imports', '0')::numeric,
		       COALESCE(aggregates->>'total_exports', '0')::numeric,
		       COALESCE(aggregates->>'data_range_start', ''),
		       COALESCE(aggregates->>'data_range_end', ''),
		       timeseries_data
		FROM portwatch_normalized_metrics
		WHERE pageid = $1
	`, pageID).Scan(&totalPC, &totalImport, &totalExport, &dateMin, &dateMax, &tsData)

	if err != nil {
		// No local data yet
		return nil, false
	}

	metrics := &PortWatchMetrics{
		TotalPortcalls: totalPC,
		TotalImports:   totalImport,
		TotalExports:   totalExport,
		DataRangeStart: dateMin,
		DataRangeEnd:   dateMax,
	}
	if totalPC > 0 && dateMin != "" && dateMax != "" {
		// Compute daily average from actual date range
		if start, err1 := time.Parse("2006-01-02", dateMin); err1 == nil {
			if end, err2 := time.Parse("2006-01-02", dateMax); err2 == nil {
				days := end.Sub(start).Hours()/24.0 + 1
				if days > 0 {
					metrics.AvgDailyPortcalls = roundTo1(float64(totalPC) / days)
				}
			}
		}
	}

	// Parse time-series data from JSONB
	ts := &PortWatchTimeSeries{}
	if tsData != nil {
		var parsed struct {
			Portcalls []TimeSeriesPoint `json:"portcalls,omitempty"`
			Imports   []TimeSeriesPoint `json:"imports,omitempty"`
			Exports   []TimeSeriesPoint `json:"exports,omitempty"`
		}
		if json.Unmarshal(tsData, &parsed) == nil {
			ts.Portcalls = parsed.Portcalls
			ts.Imports = parsed.Imports
			ts.Exports = parsed.Exports
		}
	}

	// Also try portwatch_timeseries for chart data if ts is empty
	if len(ts.Portcalls) == 0 {
		rows, err := db.Query(`
			SELECT dataset_name, date_label, value
			FROM portwatch_timeseries
			WHERE pageid = $1
			ORDER BY date_label ASC
		`, pageID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var dsName, dateLabel string
				var val float64
				if rows.Scan(&dsName, &dateLabel, &val) != nil {
					continue
				}
				pt := TimeSeriesPoint{Date: dateLabel, Value: val}
				switch dsName {
				case "portcalls", "vessel_traffic_monthly":
					ts.Portcalls = append(ts.Portcalls, pt)
				case "imports", "trade_imports":
					ts.Imports = append(ts.Imports, pt)
				case "exports", "trade_exports":
					ts.Exports = append(ts.Exports, pt)
				}
			}
		}
	}

	result.Metrics = metrics
	result.TimeSeries = ts
	return result, true
}

// fetchPortWatchFromArcGIS is the legacy ArcGIS proxy path (used when local DB has no data).
func fetchPortWatchFromArcGIS(result *PortWatchPageResult) (*PortWatchPageResult, error) {
	if result.PortID == "" {
		return result, nil
	}

	client := getPortWatchClient()
	isChokepoint := result.PortType == "chokepoint"
	serviceURL := client.basePortURL
	if isChokepoint {
		serviceURL = client.baseChokeURL
	}

	where := fmt.Sprintf("portid = '%s'", escapeArcGISString(result.PortID))
	features, err := client.fetchAll(serviceURL, where, "*", "date DESC")
	if err != nil {
		log.Printf("[portwatch] ArcGIS fallback error for %s: %v", result.PortID, err)
		return result, nil
	}

	if len(features) == 0 {
		return result, nil
	}

	metrics := &PortWatchMetrics{}
	portcallsTS := make([]TimeSeriesPoint, 0)
	importsTS := make([]TimeSeriesPoint, 0)
	exportsTS := make([]TimeSeriesPoint, 0)

	var dateMin, dateMax string
	totalPortcalls := 0
	totalImportVal := 0.0
	totalExportVal := 0.0

	for _, f := range features {
		attrs, ok := f["attributes"].(map[string]interface{})
		if !ok {
			continue
		}

		date := getAttrString(attrs, "date")
		if date == "" {
			continue
		}
		if dateMin == "" || date < dateMin {
			dateMin = date
		}
		if dateMax == "" || date > dateMax {
			dateMax = date
		}

		if isChokepoint {
			pc := getAttrInt(attrs, "n_total")
			totalPortcalls += pc
			portcallsTS = append(portcallsTS, TimeSeriesPoint{Date: date, Value: float64(pc)})
		} else {
			pc := getAttrInt(attrs, "portcalls")
			imp := getAttrFloat(attrs, "import")
			exp := getAttrFloat(attrs, "export")
			totalPortcalls += pc
			totalImportVal += imp
			totalExportVal += exp
			portcallsTS = append(portcallsTS, TimeSeriesPoint{Date: date, Value: float64(pc)})
			if imp > 0 {
				importsTS = append(importsTS, TimeSeriesPoint{Date: date, Value: imp})
			}
			if exp > 0 {
				exportsTS = append(exportsTS, TimeSeriesPoint{Date: date, Value: exp})
			}
		}
	}

	numDays := len(features)
	if numDays > 0 {
		metrics.AvgDailyPortcalls = roundTo1(float64(totalPortcalls) / float64(numDays))
	}
	metrics.TotalPortcalls = totalPortcalls
	metrics.TotalImports = totalImportVal
	metrics.TotalExports = totalExportVal
	metrics.DataRangeStart = dateMin
	metrics.DataRangeEnd = dateMax

	result.Metrics = metrics
	result.TimeSeries = &PortWatchTimeSeries{
		Portcalls: portcallsTS,
		Imports:   importsTS,
		Exports:   exportsTS,
	}

	return result, nil
}

// ──── Utility functions ────

func escapeArcGISString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func getAttrString(attrs map[string]interface{}, key string) string {
	if v, ok := attrs[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getAttrInt(attrs map[string]interface{}, key string) int {
	if v, ok := attrs[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return 0
}

func getAttrFloat(attrs map[string]interface{}, key string) float64 {
	if v, ok := attrs[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0
}

func roundTo1(v float64) float64 {
	return float64(int(v*10)) / 10
}

// ──── HTTP Handlers ────

// handleAvailableMetrics returns a list of available metric types.
func handleAvailableMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := map[string]interface{}{
		"available_metrics": []map[string]interface{}{
			{"key": "portcalls", "label": "Daily Port Calls", "type": "line", "description": "Number of vessel calls per day by type"},
			{"key": "imports", "label": "Daily Imports (tons)", "type": "line", "description": "Daily import tonnage by vessel type"},
			{"key": "exports", "label": "Daily Exports (tons)", "type": "line", "description": "Daily export tonnage by vessel type"},
			{"key": "vessel_breakdown", "label": "Vessel Composition", "type": "pie", "description": "Breakdown of vessel types by total calls"},
			{"key": "industries", "label": "Top Industries", "type": "stat", "description": "Primary industries served by this port"},
		},
		"data_sources": []map[string]interface{}{
			{"name": "Daily_Ports_Data", "type": "ArcGIS Feature Service", "coverage": "2019-01 to present", "update_frequency": "daily"},
			{"name": "Daily_Chokepoints_Data", "type": "ArcGIS Feature Service", "coverage": "2019-01 to present", "update_frequency": "daily"},
		},
		"external_data_available": []map[string]interface{}{
			{"label": "Top 5 Port Connections", "url": "https://portwatch.imf.org/", "note": "Not available via API — computed from AIS trajectory analysis by IMF PortWatch"},
			{"label": "Annual Vessel Composition", "url": "https://portwatch.imf.org/", "note": "Not available via API — rendered in PortWatch dashboard charts"},
			{"label": "Spillover Simulator", "url": "https://portwatch.imf.org/", "note": "Interactive tool on PortWatch site"},
			{"label": "Climate Scenarios", "url": "https://portwatch.imf.org/", "note": "Climate risk analysis on PortWatch site"},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// handlePortWatchData returns daily portcall/import/export data for a given pageid.
func handlePortWatchData(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		pageID := vars["pageid"]
		if pageID == "" {
			http.Error(w, "missing pageid", http.StatusBadRequest)
			return
		}

		// Check cache
		cacheKey := fmt.Sprintf("pw:data:%s", pageID)
		if cached, found := c.Get(cacheKey); found {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached.([]byte))
			return
		}

		result, err := fetchPortWatchData(db, pageID)
		if err != nil {
			log.Printf("[portwatch] data fetch error for %s: %v", pageID, err)
			// Return partial with external link
			result = &PortWatchPageResult{
				PageID:          pageID,
				ExternalURL:     fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
				UnavailableData: getDefaultUnavailableData(pageID),
			}
		}

		// Add external data reference for missing features
		result.UnavailableData = getDefaultUnavailableData(pageID)

		b, _ := json.Marshal(result)
		c.Set(cacheKey, b, 5*time.Minute)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

// handlePortWatchMetrics returns aggregated metrics for a given pageid.
func handlePortWatchMetrics(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		pageID := vars["pageid"]
		if pageID == "" {
			http.Error(w, "missing pageid", http.StatusBadRequest)
			return
		}

		cacheKey := fmt.Sprintf("pw:metrics:%s", pageID)
		if cached, found := c.Get(cacheKey); found {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached.([]byte))
			return
		}

		result, err := fetchPortWatchData(db, pageID)
		if err != nil || result.Metrics == nil {
			// Return empty metrics scaffold
			result = &PortWatchPageResult{
				PageID:      pageID,
				ExternalURL: fmt.Sprintf("https://portwatch.imf.org/pages/%s", pageID),
			}
		}

		// Strip timeseries to keep response small
		result.TimeSeries = nil

		b, _ := json.Marshal(result)
		c.Set(cacheKey, b, 5*time.Minute)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

// handlePortWatchSearch searches ports/chokepoints by name or pageid.
func handlePortWatchSearch(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			http.Error(w, "missing query param 'q'", http.StatusBadRequest)
			return
		}

		like := "%" + strings.ToLower(query) + "%"

		// Search ports
		rows, err := db.Query(`
			SELECT source_payload->>'pageid', source_payload->>'portname', 
			       COALESCE(source_payload->>'country', ''), 'port',
			       source_payload->>'portid'
			FROM ports 
			WHERE LOWER(source_payload->>'portname') LIKE $1 
			   OR LOWER(source_payload->>'pageid') LIKE $1
			LIMIT 10
		`, like)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		type SearchResult struct {
			PageID  string `json:"pageid"`
			Name    string `json:"name"`
			Country string `json:"country"`
			Type    string `json:"type"`
			PortID  string `json:"port_id"`
			URL     string `json:"url"`
		}

		results := make([]SearchResult, 0)
		for rows.Next() {
			var sr SearchResult
			if err := rows.Scan(&sr.PageID, &sr.Name, &sr.Country, &sr.Type, &sr.PortID); err != nil {
				continue
			}
			sr.URL = fmt.Sprintf("https://portwatch.imf.org/pages/%s", sr.PageID)
			results = append(results, sr)
		}

		// Also search chokepoints
		rows2, err := db.Query(`
			SELECT source_payload->>'pageid', source_payload->>'portname',
			       COALESCE(source_payload->>'country', ''), 'chokepoint',
			       source_payload->>'portid'
			FROM chokepoints 
			WHERE LOWER(source_payload->>'portname') LIKE $1
			   OR LOWER(source_payload->>'pageid') LIKE $1
			LIMIT 10
		`, like)
		if err == nil {
			defer rows2.Close()
			for rows2.Next() {
				var sr SearchResult
				if err := rows2.Scan(&sr.PageID, &sr.Name, &sr.Country, &sr.Type, &sr.PortID); err != nil {
					continue
				}
				sr.URL = fmt.Sprintf("https://portwatch.imf.org/pages/%s", sr.PageID)
				results = append(results, sr)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"query":   query,
			"results": results,
		})
	}
}

// handleTriggerScrape triggers a background scrape of PortWatch data.
func handleTriggerScrape(w http.ResponseWriter, r *http.Request) {
	// This would trigger the scraper. For now, return a reference.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":       "Scrape endpoint. Use the ingest pipeline to scrape PortWatch data.",
		"documentation": "POST /api/portwatch/scrape with JSON body {\"pageids\": [...]} to trigger scrape",
		"external":      "https://portwatch.imf.org/",
	})
}
