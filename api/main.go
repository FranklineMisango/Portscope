package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"
	cache "github.com/patrickmn/go-cache"
)

type LiveEntry struct {
	ID       int64           `json:"id"`
	MMSI     *int64          `json:"mmsi,omitempty"`
	Time     time.Time       `json:"time"`
	Payload  json.RawMessage `json:"payload"`
	Position json.RawMessage `json:"position,omitempty"`
}

type DatasetEntry struct {
	ID          int64           `json:"id"`
	SourceID    string          `json:"source_id,omitempty"`
	Name        string          `json:"name"`
	Country     string          `json:"country,omitempty"`
	ISO3        string          `json:"iso3,omitempty"`
	ObservedOn  string          `json:"observed_on,omitempty"`
	Metrics     json.RawMessage `json:"metrics,omitempty"`
	SourceValue json.RawMessage `json:"source_value,omitempty"`
	Geom        json.RawMessage `json:"geom,omitempty"`
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsMessage struct {
	Type string          `json:"type"`
	Kind string          `json:"kind,omitempty"`
	ID   int64           `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ——— AIS streaming globals ———
var (
	aisConnMu sync.Mutex
	aisConn   *websocket.Conn
	aisCtx    context.Context
	aisCancel context.CancelFunc
	aisDB     *sql.DB
)

// portBoundingBox creates a bounding box [SW, NE] around a point with given radius in km
func portBoundingBox(lat, lon, radiusKm float64) [][]float64 {
	latDelta := (radiusKm / 6371.0) * (180.0 / math.Pi)
	lonDelta := (radiusKm / 6371.0) * (180.0 / math.Pi) / math.Cos(lat*math.Pi/180.0)
	latDelta = math.Min(latDelta, 90)
	lonDelta = math.Min(lonDelta, 180)
	return [][]float64{
		{lat - latDelta, lon - lonDelta},
		{lat + latDelta, lon + lonDelta},
	}
}

// tileBBoxMercator returns the bounding box in EPSG:3857 meters for a given XYZ tile
func tileBBoxMercator(z, x, y int) (minx, miny, maxx, maxy float64) {
	n := math.Pow(2, float64(z))
	lon1 := float64(x)/n*360.0 - 180.0
	lon2 := float64(x+1)/n*360.0 - 180.0
	lat1 := tileYToLat(y, z)
	lat2 := tileYToLat(y+1, z)
	minx, miny = lonLatToMerc(lon1, lat2)
	maxx, maxy = lonLatToMerc(lon2, lat1)
	return
}

func tileYToLat(y, z int) float64 {
	n := math.Pi - (2.0*math.Pi*float64(y))/math.Pow(2, float64(z))
	lat := math.Atan(math.Sinh(n)) * 180.0 / math.Pi
	return lat
}

func lonLatToMerc(lon, lat float64) (x, y float64) {
	x = lon * 20037508.34 / 180.0
	y = math.Log(math.Tan((90.0+lat)*math.Pi/360.0)) / (math.Pi/180.0)
	y = y * 20037508.34 / 180.0
	return
}

// currentAISPort tracks which port we're currently streaming for reconnection
type aisSubscriptionState struct {
	apiKey string
	lat    float64
	lon    float64
}

var currentAISPort aisSubscriptionState
var currentAISPortMu sync.Mutex

// updateAISSubscription connects/reconnects to aisstream.io with the given bounding box for a port.
// If lat/lon are zero, it closes the AIS connection (no active watch).
// The connection is self-healing: it will automatically reconnect on failure.
func updateAISSubscription(apiKey string, lat, lon float64) {
	// Track the desired port state for reconnection
	currentAISPortMu.Lock()
	if lat == 0 && lon == 0 {
		currentAISPort = aisSubscriptionState{}
	} else {
		currentAISPort = aisSubscriptionState{apiKey: apiKey, lat: lat, lon: lon}
	}
	currentAISPortMu.Unlock()

	// Cancel any existing connection
	aisConnMu.Lock()
	if aisCancel != nil {
		aisCancel()
	}
	if aisConn != nil {
		aisConn.Close()
		aisConn = nil
	}
	aisConnMu.Unlock()

	// If no coordinates provided, we're done (disconnected)
	if lat == 0 && lon == 0 {
		log.Println("[ais] no active port subscription")
		return
	}

	// Build a bounding box around the port (20km radius)
	box := portBoundingBox(lat, lon, 20.0)
	boxes := [][][]float64{box}

	ctx, cancel := context.WithCancel(context.Background())

	aisConnMu.Lock()
	aisCtx = ctx
	aisCancel = cancel
	aisConnMu.Unlock()

	go func() {
		const maxRetryDelay = 60 * time.Second
		retryDelay := 1 * time.Second

		for {
			// Check if we've been cancelled (port deselected)
			select {
			case <-ctx.Done():
				log.Println("[ais] connection closed (context done)")
				return
			default:
			}

			log.Printf("[ais] connecting to aisstream.io for bounding box [[%v, %v], [%v, %v]]", box[0][0], box[0][1], box[1][0], box[1][1])

			u := "wss://stream.aisstream.io/v0/stream"
			c, _, err := websocket.DefaultDialer.Dial(u, nil)
			if err != nil {
				log.Printf("[ais] dial error: %v (retrying in %v)", err, retryDelay)
				select {
				case <-ctx.Done():
					return
				case <-time.After(retryDelay):
				}
				retryDelay = minDuration(retryDelay*2, maxRetryDelay)
				continue
			}
			// Reset retry delay on successful connection
			retryDelay = 1 * time.Second

			// Send subscription message
			subMsg := map[string]interface{}{
				"APIKey":             apiKey,
				"BoundingBoxes":      boxes,
				"FilterMessageTypes": []string{"PositionReport"},
			}
			if err := c.WriteJSON(subMsg); err != nil {
				log.Printf("[ais] write subscription error: %v", err)
				c.Close()
				continue
			}
			log.Printf("[ais] subscription sent, receiving AIS data...")

			aisConnMu.Lock()
			aisConn = c
			aisConnMu.Unlock()

			// Set keepalive
			pingTicker := time.NewTicker(30 * time.Second)
			c.SetPongHandler(func(string) error {
				return c.SetReadDeadline(time.Now().Add(60 * time.Second))
			})
			c.SetReadDeadline(time.Now().Add(60 * time.Second))

			// Batch insert to reduce DB pressure
			batch := make([]map[string]interface{}, 0, 100)
			flushBatch := func() {
				if len(batch) == 0 {
					return
				}
				if err := insertTrafficBatch(context.Background(), batch); err != nil {
					log.Printf("[ais] batch insert error: %v", err)
				}
				batch = batch[:0]
			}

			batchTicker := time.NewTicker(5 * time.Second)

			streamActive := true
			for streamActive {
				select {
				case <-ctx.Done():
					flushBatch()
					c.Close()
					aisConnMu.Lock()
					aisConn = nil
					aisConnMu.Unlock()
					pingTicker.Stop()
					batchTicker.Stop()
					log.Println("[ais] connection closed (context done)")
					return
				case <-batchTicker.C:
					flushBatch()
				case <-pingTicker.C:
					if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
						log.Printf("[ais] ping error: %v", err)
					}
				default:
				}

				_, message, err := c.ReadMessage()
				if err != nil {
					flushBatch()
					log.Printf("[ais] read error: %v", err)
					c.Close()
					aisConnMu.Lock()
					aisConn = nil
					aisConnMu.Unlock()
					pingTicker.Stop()
					batchTicker.Stop()
					streamActive = false
					continue
				}

				var msg map[string]interface{}
				if err := json.Unmarshal(message, &msg); err != nil {
					continue
				}

				// Check for error messages from aisstream
				if errStr, ok := msg["error"]; ok {
					log.Printf("[ais] aisstream error: %v", errStr)
					// If we get an auth error, don't retry
					if errMsg, ok := errStr.(string); ok && (strings.Contains(errMsg, "Invalid API Key") || strings.Contains(errMsg, "API key")) {
						log.Printf("[ais] fatal auth error, stopping reconnection")
						return
					}
					continue
				}

				batch = append(batch, msg)
				if len(batch) >= 100 {
					flushBatch()
				}
			}
		}
	}()
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// insertTrafficBatch inserts a batch of AIS messages into traffic_logs
func insertTrafficBatch(ctx context.Context, messages []map[string]interface{}) error {
	if len(messages) == 0 || aisDB == nil {
		return nil
	}

	tx, err := aisDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO traffic_logs (mmsi, payload, position, speed_kts, course_deg, event_time) 
		VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326), $5, $6, $7)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, msg := range messages {
		msgType, _ := msg["MessageType"].(string)
		if msgType != "PositionReport" {
			continue
		}

		// Extract the inner PositionReport
		messageField, _ := msg["Message"].(map[string]interface{})
		if messageField == nil {
			continue
		}
		prRaw, ok := messageField["PositionReport"].(map[string]interface{})
		if !ok {
			continue
		}

		// Get MMSI
		mmsi := int64(0)
		if userID, ok := prRaw["UserID"].(float64); ok {
			mmsi = int64(userID)
		}

		// Get coordinates
		lat, _ := prRaw["Latitude"].(float64)
		lon, _ := prRaw["Longitude"].(float64)
		if lat == 0 && lon == 0 {
			// Try metadata
			if meta, ok := msg["MetaData"].(map[string]interface{}); ok {
				if mlat, ok := meta["latitude"].(float64); ok {
					lat = mlat
				}
				if mlon, ok := meta["longitude"].(float64); ok {
					lon = mlon
				}
			}
		}
		if lat == 0 && lon == 0 {
			continue
		}

		sog, _ := prRaw["Sog"].(float64)
		cog, _ := prRaw["Cog"].(float64)

		eventTime := time.Now()
		if ts, ok := prRaw["Timestamp"].(float64); ok && ts > 0 && ts <= 60 {
			now := time.Now().Truncate(time.Minute)
			eventTime = now.Add(time.Duration(ts) * time.Second)
		}

		payloadBytes, _ := json.Marshal(msg)

		_, err = stmt.ExecContext(ctx,
			mmsi,
			string(payloadBytes),
			lon, lat,
			sog,
			cog,
			eventTime,
		)
		if err != nil {
			log.Printf("[ais] insert error for mmsi %d: %v", mmsi, err)
			continue
		}
	}

	return tx.Commit()
}

// ——— end AIS streaming globals ———

func loadPortRecordByID(db *sql.DB, id int64) (DatasetEntry, error) {
	var out DatasetEntry
	var country, iso3, observedOn, geom sql.NullString
	row := db.QueryRow(`SELECT id, name, country, iso3, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM ports WHERE id=$1`, id)
	if err := row.Scan(&out.ID, &out.Name, &country, &iso3, &observedOn, &out.SourceValue, &geom); err != nil {
		return out, err
	}
	if country.Valid {
		out.Country = country.String
	}
	if iso3.Valid {
		out.ISO3 = iso3.String
	}
	if observedOn.Valid {
		out.ObservedOn = observedOn.String
	}
	if geom.Valid {
		out.Geom = json.RawMessage(geom.String)
	}
	return out, nil
}

func loadPortRecordByName(db *sql.DB, name string) (DatasetEntry, error) {
	var out DatasetEntry
	var country, iso3, observedOn, geom sql.NullString
	row := db.QueryRow(`SELECT id, name, country, iso3, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM ports WHERE name=$1`, name)
	if err := row.Scan(&out.ID, &out.Name, &country, &iso3, &observedOn, &out.SourceValue, &geom); err != nil {
		return out, err
	}
	if country.Valid {
		out.Country = country.String
	}
	if iso3.Valid {
		out.ISO3 = iso3.String
	}
	if observedOn.Valid {
		out.ObservedOn = observedOn.String
	}
	if geom.Valid {
		out.Geom = json.RawMessage(geom.String)
	}
	return out, nil
}

func loadChokepointRecordByID(db *sql.DB, id int64) (DatasetEntry, error) {
	var out DatasetEntry
	var observedOn, geom sql.NullString
	row := db.QueryRow(`SELECT id, name, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM chokepoints WHERE id=$1`, id)
	if err := row.Scan(&out.ID, &out.Name, &observedOn, &out.SourceValue, &geom); err != nil {
		return out, err
	}
	if observedOn.Valid {
		out.ObservedOn = observedOn.String
	}
	if geom.Valid {
		out.Geom = json.RawMessage(geom.String)
	}
	return out, nil
}

func loadChokepointRecordByName(db *sql.DB, name string) (DatasetEntry, error) {
	var out DatasetEntry
	var observedOn, geom sql.NullString
	row := db.QueryRow(`SELECT id, name, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM chokepoints WHERE name=$1`, name)
	if err := row.Scan(&out.ID, &out.Name, &observedOn, &out.SourceValue, &geom); err != nil {
		return out, err
	}
	if observedOn.Valid {
		out.ObservedOn = observedOn.String
	}
	if geom.Valid {
		out.Geom = json.RawMessage(geom.String)
	}
	return out, nil
}

// loadPortCoordsByName returns lat, lon for a port name
func loadPortCoordsByName(db *sql.DB, name string) (lat, lon float64, err error) {
	// Try PostGIS geometry first
	err = db.QueryRow(`SELECT ST_Y(geom), ST_X(geom) FROM ports WHERE name=$1 AND geom IS NOT NULL`, name).Scan(&lat, &lon)
	if err == nil {
		return
	}
	// Fallback: extract lat/lon from source_payload JSONB (ArcGIS data often includes these)
	err = db.QueryRow(`SELECT (source_payload->>'lat')::numeric, (source_payload->>'lon')::numeric FROM ports WHERE name=$1 AND source_payload ? 'lat' AND source_payload ? 'lon'`, name).Scan(&lat, &lon)
	if err == nil {
		return lat, lon, nil
	}
	return
}

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@db:5432/postgres?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Store DB reference for AIS streaming
	aisDB = db

	// Load the PortWatch pageid → portid mapping into memory
	loadPortWatchMapping(db)

	// ——— AIS streaming setup ———
	aisAPIKey := os.Getenv("AISSTREAM_API_KEY")

	// simple in-memory cache with 30s default TTL and 1m cleanup
	c = cache.New(30*time.Second, 1*time.Minute)

	r := mux.NewRouter()

	// Vector tile endpoints (ports and chokepoints)
	r.HandleFunc("/tiles/ports/{z:[0-9]+}/{x:[0-9]+}/{y:[0-9]+}.pbf", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		z, _ := strconv.Atoi(vars["z"])
		x, _ := strconv.Atoi(vars["x"])
		y, _ := strconv.Atoi(vars["y"])
		minx, miny, maxx, maxy := tileBBoxMercator(z, x, y)

		qry := `WITH mvtgeom AS (
			SELECT id, name, ST_AsMVTGeom(ST_Transform(geom,3857), ST_MakeEnvelope($1,$2,$3,$4,3857)) AS geom
			FROM ports
			WHERE ST_Intersects(ST_Transform(geom,3857), ST_MakeEnvelope($1,$2,$3,$4,3857))
		) SELECT ST_AsMVT(mvtgeom, 'ports', 4096, 'geom') FROM mvtgeom;`

		var buf []byte
		row := db.QueryRow(qry, minx, miny, maxx, maxy)
		if err := row.Scan(&buf); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Gzip the buffer on the fly
		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		if _, err := gz.Write(buf); err == nil {
			gz.Close()
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeContent(w, r, "ports.pbf", time.Now(), bytes.NewReader(gzBuf.Bytes()))
			return
		}
		// fallback
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, "ports.pbf", time.Now(), bytes.NewReader(buf))
	}).Methods("GET")

	r.HandleFunc("/tiles/chokepoints/{z:[0-9]+}/{x:[0-9]+}/{y:[0-9]+}.pbf", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		z, _ := strconv.Atoi(vars["z"])
		x, _ := strconv.Atoi(vars["x"])
		y, _ := strconv.Atoi(vars["y"])
		minx, miny, maxx, maxy := tileBBoxMercator(z, x, y)

		qry := `WITH mvtgeom AS (
			SELECT id, name, ST_AsMVTGeom(ST_Transform(geom,3857), ST_MakeEnvelope($1,$2,$3,$4,3857)) AS geom
			FROM chokepoints
			WHERE ST_Intersects(ST_Transform(geom,3857), ST_MakeEnvelope($1,$2,$3,$4,3857))
		) SELECT ST_AsMVT(mvtgeom, 'chokepoints', 4096, 'geom') FROM mvtgeom;`

		var buf []byte
		row := db.QueryRow(qry, minx, miny, maxx, maxy)
		if err := row.Scan(&buf); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		if _, err := gz.Write(buf); err == nil {
			gz.Close()
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeContent(w, r, "chokepoints.pbf", time.Now(), bytes.NewReader(gzBuf.Bytes()))
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, "chokepoints.pbf", time.Now(), bytes.NewReader(buf))
	}).Methods("GET")

	// simple API key auth middleware
	apiKey := os.Getenv("API_KEY")
	rateLimitPerMin := 60
	if v := os.Getenv("RATE_LIMIT_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rateLimitPerMin = n
		}
	}
	rl := newRateLimiter(rateLimitPerMin)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-KEY, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			if req.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// simple auth: header X-API-KEY
			if apiKey != "" {
				k := req.Header.Get("X-API-KEY")
				if k == "" || k != apiKey {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			// rate limit per API key or IP
			key := req.Header.Get("X-API-KEY")
			if key == "" {
				key = req.RemoteAddr
			}
			if !rl.Allow(key) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	// PortWatch pageid redirect (kept for backwards compat)
	r.HandleFunc("/portwatch/{pageid}", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		pageID := vars["pageid"]
		if pageID == "" {
			http.Error(w, "missing pageid", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "https://portwatch.imf.org/pages/"+pageID, http.StatusFound)
	}).Methods("GET")

	// ─── PortWatch Data API endpoints ───
	r.HandleFunc("/api/portwatch/metrics", handleAvailableMetrics).Methods("GET")
	r.HandleFunc("/api/portwatch/{pageid}/data", handlePortWatchData(db)).Methods("GET")
	r.HandleFunc("/api/portwatch/{pageid}/metrics", handlePortWatchMetrics(db)).Methods("GET")
	r.HandleFunc("/api/portwatch/search", handlePortWatchSearch(db)).Methods("GET")
	r.HandleFunc("/api/portwatch/scrape", handleTriggerScrape).Methods("POST")

	r.HandleFunc("/port/{id:[0-9]+}/live", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		idStr := vars["id"]
		id, _ := strconv.ParseInt(idStr, 10, 64)

		// radius meters (validate)
		radius := 5000
		if v := r.URL.Query().Get("radius"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200000 {
				radius = n
			}
		}

		// lookback minutes (validate)
		lookback := 30
		if v := r.URL.Query().Get("mins"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*60*365 {
				lookback = n
			}
		}
		lookbackInterval := fmt.Sprintf("%d minutes", lookback)

		// pagination
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		cacheKey := fmt.Sprintf("live:%d:%d:%d:%d:%d", id, radius, lookback, limit, offset)
		if cached, found := c.Get(cacheKey); found {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached.([]byte))
			return
		}

		q := `SELECT id, mmsi, event_time, payload, ST_AsGeoJSON(position) AS position
			FROM traffic_logs
			WHERE position IS NOT NULL
			  AND ST_DWithin(position::geography, (SELECT geom FROM ports WHERE id=$1)::geography, $2)
			  AND event_time >= now() - $3::interval
			ORDER BY event_time DESC
			LIMIT $4 OFFSET $5`

		rows, err := db.Query(q, id, float64(radius), lookbackInterval, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := make([]LiveEntry, 0)
		for rows.Next() {
			var e LiveEntry
			var mmsi sql.NullInt64
			var pos sql.NullString
			if err := rows.Scan(&e.ID, &mmsi, &e.Time, &e.Payload, &pos); err != nil {
				log.Println("scan error:", err)
				continue
			}
			if mmsi.Valid {
				e.MMSI = &mmsi.Int64
			}
			if pos.Valid {
				e.Position = json.RawMessage(pos.String)
			}
			out = append(out, e)
		}
		b, _ := json.Marshal(out)
		c.Set(cacheKey, b, cache.DefaultExpiration)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}).Methods("GET")

	r.HandleFunc("/ports", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT id, name, country, iso3, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM ports ORDER BY name`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := make([]DatasetEntry, 0)
		for rows.Next() {
			var p DatasetEntry
			var country, iso3, observedOn, geom sql.NullString
			if err := rows.Scan(&p.ID, &p.Name, &country, &iso3, &observedOn, &p.SourceValue, &geom); err != nil {
				log.Println("scan ports error:", err)
				continue
			}
			if country.Valid {
				p.Country = country.String
			}
			if iso3.Valid {
				p.ISO3 = iso3.String
			}
			if observedOn.Valid {
				p.ObservedOn = observedOn.String
			}
			if geom.Valid {
				p.Geom = json.RawMessage(geom.String)
			}
			out = append(out, p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}).Methods("GET")

	r.HandleFunc("/chokepoints", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT id, name, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM chokepoints ORDER BY name`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := make([]DatasetEntry, 0)
		for rows.Next() {
			var p DatasetEntry
			var observedOn, geom sql.NullString
			if err := rows.Scan(&p.ID, &p.Name, &observedOn, &p.SourceValue, &geom); err != nil {
				log.Println("scan chokepoints error:", err)
				continue
			}
			if observedOn.Valid {
				p.ObservedOn = observedOn.String
			}
			if geom.Valid {
				p.Geom = json.RawMessage(geom.String)
			}
			out = append(out, p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}).Methods("GET")

	r.HandleFunc("/ws/updates", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("ws upgrade error:", err)
			return
		}
		defer conn.Close()

		sendSnapshot := func() {
			ports := make([]DatasetEntry, 0)
			if rows, err := db.Query(`SELECT id, name, country, iso3, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM ports ORDER BY name`); err == nil {
				defer rows.Close()
				for rows.Next() {
					var p DatasetEntry
					var country, iso3, observedOn, geom sql.NullString
					if err := rows.Scan(&p.ID, &p.Name, &country, &iso3, &observedOn, &p.SourceValue, &geom); err == nil {
						if country.Valid {
							p.Country = country.String
						}
						if iso3.Valid {
							p.ISO3 = iso3.String
						}
						if observedOn.Valid {
							p.ObservedOn = observedOn.String
						}
						if geom.Valid {
							p.Geom = json.RawMessage(geom.String)
						}
						ports = append(ports, p)
					}
				}
			}
			chokepoints := make([]DatasetEntry, 0)
			if rows, err := db.Query(`SELECT id, name, source_date::text, source_payload, ST_AsGeoJSON(geom) FROM chokepoints ORDER BY name`); err == nil {
				defer rows.Close()
				for rows.Next() {
					var p DatasetEntry
					var observedOn, geom sql.NullString
					if err := rows.Scan(&p.ID, &p.Name, &observedOn, &p.SourceValue, &geom); err == nil {
						if observedOn.Valid {
							p.ObservedOn = observedOn.String
						}
						if geom.Valid {
							p.Geom = json.RawMessage(geom.String)
						}
						chokepoints = append(chokepoints, p)
					}
				}
			}
			payload, _ := json.Marshal(map[string]interface{}{
				"type":        "snapshot",
				"ports":       ports,
				"chokepoints": chokepoints,
			})
			_ = conn.WriteMessage(websocket.TextMessage, payload)
		}

		sendSnapshot()
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		})

		pingTicker := time.NewTicker(45 * time.Second)
		defer pingTicker.Stop()

		for {
			select {
			case <-pingTicker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			default:
			}

			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var incoming wsMessage
			if err := json.Unmarshal(msg, &incoming); err != nil {
				continue
			}
			if incoming.Type != "select" || (incoming.ID == 0 && incoming.Name == "") {
				continue
			}

			if incoming.Kind == "port" {
				if incoming.Name != "" {
					if record, err := loadPortRecordByName(db, incoming.Name); err == nil {
						if payload, err := json.Marshal(wsMessage{Type: "selected_record", Kind: "port", ID: record.ID, Name: record.Name, Data: mustJSON(record)}); err == nil {
							_ = conn.WriteMessage(websocket.TextMessage, payload)
						}
					}
				} else if record, err := loadPortRecordByID(db, incoming.ID); err == nil {
					if payload, err := json.Marshal(wsMessage{Type: "selected_record", Kind: "port", ID: record.ID, Data: mustJSON(record)}); err == nil {
						_ = conn.WriteMessage(websocket.TextMessage, payload)
					}
				}

				// ——— Update AIS subscription for this port ———
				if aisAPIKey != "" {
					portName := incoming.Name
					if portName == "" {
						portName = "unknown"
					}
					lat, lon, err := loadPortCoordsByName(db, portName)
					if err == nil {
						log.Printf("[ais] updating subscription to port: %s (%.4f, %.4f)", portName, lat, lon)
						go updateAISSubscription(aisAPIKey, lat, lon)
					} else {
						log.Printf("[ais] could not load coords for port '%s': %v", portName, err)
					}
				}
			}
			if incoming.Kind == "chokepoint" {
				if incoming.Name != "" {
					if record, err := loadChokepointRecordByName(db, incoming.Name); err == nil {
						if payload, err := json.Marshal(wsMessage{Type: "selected_record", Kind: "chokepoint", ID: record.ID, Name: record.Name, Data: mustJSON(record)}); err == nil {
							_ = conn.WriteMessage(websocket.TextMessage, payload)
						}
					}
				} else if record, err := loadChokepointRecordByID(db, incoming.ID); err == nil {
					if payload, err := json.Marshal(wsMessage{Type: "selected_record", Kind: "chokepoint", ID: record.ID, Data: mustJSON(record)}); err == nil {
						_ = conn.WriteMessage(websocket.TextMessage, payload)
					}
				}
			}
		}
	}).Methods("GET")

	// Name-based traffic endpoint (used by frontend which doesn't have DB ids)
	r.HandleFunc("/traffic", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}

		rangeParam := r.URL.Query().Get("range")
		if rangeParam == "" {
			rangeParam = "30d"
		}
		var interval string
		switch rangeParam {
		case "30d":
			interval = "30 days"
		case "7d":
			interval = "7 days"
		case "1y":
			interval = "365 days"
		default:
			interval = "30 days"
		}

		radius := 5000
		if v := r.URL.Query().Get("radius"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200000 {
				radius = n
			}
		}

		q := `SELECT date_trunc('day', event_time) AS day, count(*) AS cnt
			FROM traffic_logs
			WHERE position IS NOT NULL
			  AND ST_DWithin(position::geography, (SELECT geom FROM ports WHERE name=$1)::geography, $2)
			  AND event_time >= now() - ($3)::interval
			GROUP BY day
			ORDER BY day`

		// Also try chokepoints table
		q2 := `SELECT date_trunc('day', event_time) AS day, count(*) AS cnt
			FROM traffic_logs
			WHERE position IS NOT NULL
			  AND ST_DWithin(position::geography, (SELECT geom FROM chokepoints WHERE name=$1)::geography, $2)
			  AND event_time >= now() - ($3)::interval
			GROUP BY day
			ORDER BY day`

		type DayCount struct {
			Day   time.Time `json:"day"`
			Count int64     `json:"count"`
		}

		// Try ports first, then chokepoints
		res := make([]DayCount, 0)
		rows, err := db.Query(q, name, float64(radius), interval)
		if err != nil {
			rows, err = db.Query(q2, name, float64(radius), interval)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		defer rows.Close()
		for rows.Next() {
			var d DayCount
			if err := rows.Scan(&d.Day, &d.Count); err != nil {
				log.Println("scan error:", err)
				continue
			}
			res = append(res, d)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	}).Methods("GET")

	// Also keep the id-based endpoint
	r.HandleFunc("/port/{id:[0-9]+}/traffic", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		idStr := vars["id"]
		id, _ := strconv.ParseInt(idStr, 10, 64)

		rangeParam := r.URL.Query().Get("range")
		if rangeParam == "" {
			rangeParam = "30d"
		}
		var interval string
		switch rangeParam {
		case "30d":
			interval = "30 days"
		case "7d":
			interval = "7 days"
		case "1y":
			interval = "365 days"
		default:
			interval = "30 days"
		}

		// radius meters (validate)
		radius := 5000
		if v := r.URL.Query().Get("radius"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200000 {
				radius = n
			}
		}

		q := `SELECT date_trunc('day', event_time) AS day, count(*) AS cnt
			FROM traffic_logs
			WHERE position IS NOT NULL
			  AND ST_DWithin(position::geography, (SELECT geom FROM ports WHERE id=$1)::geography, $2)
			  AND event_time >= now() - ($3)::interval
			GROUP BY day
			ORDER BY day`
		rows, err := db.Query(q, id, float64(radius), interval)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		type DayCount struct {
			Day   time.Time `json:"day"`
			Count int64     `json:"count"`
		}
		res := make([]DayCount, 0)
		for rows.Next() {
			var d DayCount
			if err := rows.Scan(&d.Day, &d.Count); err != nil {
				log.Println("scan error:", err)
				continue
			}
			res = append(res, d)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	}).Methods("GET")

	srv := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}
	log.Println("API listening on :8080")
	log.Fatal(srv.ListenAndServe())
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(b)
}

// simple token-bucket rate limiter per key
type rateLimiter struct {
	ratePerMin int
	tokens     map[string]float64
	last       map[string]time.Time
}

func newRateLimiter(ratePerMin int) *rateLimiter {
	return &rateLimiter{ratePerMin: ratePerMin, tokens: make(map[string]float64), last: make(map[string]time.Time)}
}

func (r *rateLimiter) Allow(key string) bool {
	now := time.Now()
	tokens := r.tokens[key]
	last := r.last[key]
	if last.IsZero() {
		tokens = float64(r.ratePerMin)
	} else {
		elapsed := now.Sub(last).Seconds()
		refill := elapsed * (float64(r.ratePerMin) / 60.0)
		tokens += refill
		if tokens > float64(r.ratePerMin) {
			tokens = float64(r.ratePerMin)
		}
	}
	if tokens < 1.0 {
		r.tokens[key] = tokens
		r.last[key] = now
		return false
	}
	r.tokens[key] = tokens - 1.0
	r.last[key] = now
	return true
}
