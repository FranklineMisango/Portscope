package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"crypto/tls"
	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

var (
	processedCounter = prometheus.NewCounter(prometheus.CounterOpts{Name: "ais_consumer_processed_total", Help: "Total processed messages"})
	errorCounter     = prometheus.NewCounter(prometheus.CounterOpts{Name: "ais_consumer_errors_total", Help: "Total processing errors"})
	dlqCounter       = prometheus.NewCounter(prometheus.CounterOpts{Name: "ais_consumer_dlq_total", Help: "Total messages dead-lettered"})
	claimedCounter   = prometheus.NewCounter(prometheus.CounterOpts{Name: "ais_consumer_claimed_total", Help: "Total pending messages claimed"})
)

func init() {
	prometheus.MustRegister(processedCounter, errorCounter, dlqCounter, claimedCounter)
}

func toFloat64(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func unwrapAISMessage(m map[string]interface{}) map[string]interface{} {
	message, ok := m["Message"].(map[string]interface{})
	if !ok || len(message) == 0 {
		return nil
	}
	for _, value := range message {
		if inner, ok := value.(map[string]interface{}); ok {
			return inner
		}
	}
	return nil
}

func tryLatLon(m map[string]interface{}) (lat, lon float64, ok bool) {
	if inner := unwrapAISMessage(m); inner != nil {
		m = inner
	}
	if v, okx := m["Latitude"]; okx {
		if latf, okf := toFloat64(v); okf {
			if w, wok := m["Longitude"]; wok {
				if lonf, ok2 := toFloat64(w); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	if v, okx := m["latitude"]; okx {
		if latf, okf := toFloat64(v); okf {
			if w, wok := m["longitude"]; wok {
				if lonf, ok2 := toFloat64(w); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	if v, okx := m["lat"]; okx {
		if latf, okf := toFloat64(v); okf {
			if w, wok := m["lon"]; wok {
				if lonf, ok2 := toFloat64(w); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	if pos, ok := m["position"]; ok {
		if pm, okm := pos.(map[string]interface{}); okm {
			return tryLatLon(pm)
		}
	}
	if meta, ok := m["MetaData"]; ok {
		if pm, okm := meta.(map[string]interface{}); okm {
			if latf, okf := toFloat64(pm["Latitude"]); okf {
				if lonf, ok2 := toFloat64(pm["Longitude"]); ok2 {
					return latf, lonf, true
				}
			}
			if latf, okf := toFloat64(pm["latitude"]); okf {
				if lonf, ok2 := toFloat64(pm["longitude"]); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	if meta, ok := m["Metadata"]; ok {
		if pm, okm := meta.(map[string]interface{}); okm {
			if latf, okf := toFloat64(pm["Latitude"]); okf {
				if lonf, ok2 := toFloat64(pm["Longitude"]); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	return 0, 0, false
}

func parseMMSI(m map[string]interface{}) (int64, bool) {
	if inner := unwrapAISMessage(m); inner != nil {
		m = inner
	}
	keys := []string{"mmsi", "MMSI", "Mmsi", "UserID"}
	for _, key := range keys {
		if value, ok := m[key]; ok {
			switch typed := value.(type) {
			case float64:
				return int64(typed), true
			case int64:
				return typed, true
			case int:
				return int64(typed), true
			case string:
				trimmed := strings.TrimSpace(typed)
				if trimmed == "" {
					continue
				}
				if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
					return parsed, true
				}
			}
		}
	}
	return 0, false
}

func ensureGroup(ctx context.Context, rdb *redis.Client, stream, group string) error {
	err := rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func reverseGeocode(lat, lon float64) (string, error) {
	base := "https://nominatim.openstreetmap.org/reverse"
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("format", "jsonv2")
	q.Set("lat", fmt.Sprintf("%f", lat))
	q.Set("lon", fmt.Sprintf("%f", lon))
	u.RawQuery = q.Encode()

	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "Portscope-Ingest/1.0 (+https://github.com)")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("reverse geocode %d: %s", resp.StatusCode, string(body))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if name, ok := out["display_name"]; ok {
		return fmt.Sprintf("%v", name), nil
	}
	return "", nil
}

func startHTTPServer(addr string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Printf("starting metrics server on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Println("metrics server error:", err)
		}
	}()
}

func parseBoundingBoxes() [][][]float64 {
	if raw := os.Getenv("AISSTREAM_BBOXES_JSON"); raw != "" {
		var boxes [][][]float64
		if err := json.Unmarshal([]byte(raw), &boxes); err == nil && len(boxes) > 0 {
			return boxes
		}
		log.Println("invalid AISSTREAM_BBOXES_JSON, falling back to global bounding box")
	}
	return [][][]float64{{{-90, -180}, {90, 180}}}
}

func parseMessageTypes() []string {
	raw := os.Getenv("AISSTREAM_FILTER_MESSAGE_TYPES")
	if raw == "" {
		return []string{"PositionReport"}
	}
	parts := strings.Split(raw, ",")
	messageTypes := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			messageTypes = append(messageTypes, trimmed)
		}
	}
	if len(messageTypes) == 0 {
		return []string{"PositionReport"}
	}
	return messageTypes
}

func streamAISFeed(ctx context.Context, apiKey, redisAddr, redisStream, pgDsn string) error {
	u := url.URL{Scheme: "wss", Host: "stream.aisstream.io", Path: "/v0/stream"}
	log.Printf("connecting to %s", u.String())

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()

	db, err := sql.Open("pgx", pgDsn)
	if err != nil {
		return fmt.Errorf("postgres open: %w", err)
	}
	defer db.Close()

	dialer := websocket.DefaultDialer
	if skip := os.Getenv("AISSTREAM_INSECURE_SKIP_VERIFY"); strings.ToLower(skip) == "true" {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		log.Println("AISSTREAM_INSECURE_SKIP_VERIFY=true; skipping TLS verification (testing only)")
	}
	c, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()

	subscription := map[string]interface{}{
		"APIKey":           apiKey,
		"BoundingBoxes":    parseBoundingBoxes(),
		"FilterMessageTypes": parseMessageTypes(),
	}
	if err := c.WriteJSON(subscription); err != nil {
		return fmt.Errorf("write subscription: %w", err)
	}

	log.Println("subscription sent, publishing AIS data to Redis stream and Postgres...")

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read error:", err)
				return
			}

			if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: redisStream, Values: map[string]interface{}{"payload": string(message)}}).Err(); err != nil {
				log.Println("redis XAdd error:", err)
			}

			var m map[string]interface{}
			if err := json.Unmarshal(message, &m); err == nil {
				lat, lon, ok := tryLatLon(m)
				if ok {
					_, err = db.ExecContext(ctx, `INSERT INTO ais_messages (payload, position) VALUES ($1, ST_SetSRID(ST_MakePoint($2, $3), 4326))`, message, lon, lat)
				} else {
					_, err = db.ExecContext(ctx, `INSERT INTO ais_messages (payload) VALUES ($1)`, message)
				}
				if err != nil {
					log.Println("postgres insert error:", err)
				}

				if ok {
					if name, err := reverseGeocode(lat, lon); err == nil && name != "" {
						_, _ = db.ExecContext(ctx, `INSERT INTO ports (name, geom, last_seen) VALUES ($1, ST_SetSRID(ST_MakePoint($2,$3),4326), now()) ON CONFLICT (name) DO UPDATE SET last_seen = EXCLUDED.last_seen, geom = COALESCE(EXCLUDED.geom, ports.geom)`, name, lon, lat)
					}
				}
			} else {
				_, err = db.ExecContext(ctx, `INSERT INTO ais_messages (payload) VALUES ($1)`, message)
				if err != nil {
					log.Println("postgres insert fallback error:", err)
				}
			}

			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("interrupt, shutting down")
		_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
		}
		return nil
	case <-readDone:
		return fmt.Errorf("ais websocket closed")
	}
}

func main() {
	pgDsn := os.Getenv("POSTGRES_DSN")
	if pgDsn == "" {
		pgDsn = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}

	metricsAddr := ":9090"
	if v := os.Getenv("METRICS_ADDR"); v != "" {
		metricsAddr = v
	}
	startHTTPServer(metricsAddr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for {
		if err := runArcGISSyncLoop(ctx, pgDsn); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Println("ArcGIS sync error:", err)
			time.Sleep(30 * time.Second)
			continue
		}
		return
	}
}
