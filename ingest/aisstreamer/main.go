package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type PortRecord struct {
	Name string
	Lat  float64
	Lon  float64
}

func buildBoundingBoxes(ports []PortRecord, radiusKm float64) [][][]float64 {
	boxes := make([][][]float64, 0, len(ports))
	for _, p := range ports {
		box := portBoundingBox(p.Lat, p.Lon, radiusKm)
		boxes = append(boxes, box)
	}
	return boxes
}

// portBoundingBox creates a bounding box [SW corner, NE corner] around a point
func portBoundingBox(lat, lon, radiusKm float64) [][]float64 {
	// Earth radius ~6371 km
	latDelta := (radiusKm / 6371.0) * (180.0 / math.Pi)
	lonDelta := (radiusKm / 6371.0) * (180.0 / math.Pi) / math.Cos(lat*math.Pi/180.0)

	latDelta = math.Min(latDelta, 90)
	lonDelta = math.Min(lonDelta, 180)

	swLat := lat - latDelta
	swLon := lon - lonDelta
	neLat := lat + latDelta
	neLon := lon + lonDelta

	return [][]float64{
		{swLat, swLon},
		{neLat, neLon},
	}
}

type AisMessage struct {
	MessageType string          `json:"MessageType"`
	Message     json.RawMessage `json:"Message"`
	MetaData    json.RawMessage `json:"MetaData"`
}

type Metadata struct {
	MMSI     int64   `json:"MMSI"`
	ShipName string  `json:"ShipName"`
	Latitude float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type PositionReport struct {
	UserID    int64   `json:"UserID"`
	Latitude  float64 `json:"Latitude"`
	Longitude float64 `json:"Longitude"`
	Sog       float64 `json:"Sog"`
	Cog       float64 `json:"Cog"`
	TrueHeading int   `json:"TrueHeading"`
	NavigationalStatus int `json:"NavigationalStatus"`
	RateOfTurn int    `json:"RateOfTurn"`
	Timestamp int     `json:"Timestamp"`
}

func subscribeMessage(apiKey string, boxes [][][]float64) map[string]interface{} {
	return map[string]interface{}{
		"APIKey":           apiKey,
		"BoundingBoxes":    boxes,
		"FilterMessageTypes": []string{"PositionReport"},
	}
}

func main() {
	apiKey := os.Getenv("AISSTREAM_API_KEY")
	if apiKey == "" {
		log.Fatal("AISSTREAM_API_KEY environment variable required")
	}

	pgDsn := os.Getenv("POSTGRES_DSN")
	if pgDsn == "" {
		pgDsn = "postgres://postgres:postgres@db:5432/postgres?sslmode=disable"
	}

	// Radius in km around each port
	radiusKm := 20.0
	if v := os.Getenv("AISSTREAM_RADIUS_KM"); v != "" {
		if f, err := fmt.Sscanf(v, "%f", &radiusKm); err != nil || f != 1 {
			radiusKm = 20.0
		}
	}
	// Clamp
	if radiusKm < 1 {
		radiusKm = 1
	}
	if radiusKm > 200 {
		radiusKm = 200
	}

	refreshInterval := 30 * time.Minute
	if v := os.Getenv("AISSTREAM_REFRESH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			refreshInterval = d
		}
	}

	log.Printf("aisstreamer starting: radius=%.1fkm, refresh=%s", radiusKm, refreshInterval)

	db, err := sql.Open("pgx", pgDsn)
	if err != nil {
		log.Fatal("db open:", err)
	}
	defer db.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Periodic refresh of port list and reconnection
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ports, err := loadPorts(db)
		if err != nil {
			log.Printf("load ports error: %v, retrying in 30s", err)
			time.Sleep(30 * time.Second)
			continue
		}
		log.Printf("loaded %d ports from database", len(ports))

		if len(ports) == 0 {
			log.Println("no ports found, retrying in 30s")
			time.Sleep(30 * time.Second)
			continue
		}

		boxes := buildBoundingBoxes(ports, radiusKm)
		log.Printf("built %d bounding boxes", len(boxes))

		if err := streamAIS(ctx, db, apiKey, boxes); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("ais stream disconnected: %v, reconnecting in 10s", err)
			time.Sleep(10 * time.Second)
			continue
		}

		// If we get here, the stream ended gracefully; refresh ports and reconnect
		log.Println("stream ended, refreshing ports...")
		time.Sleep(5 * time.Second)
	}
}

func loadPorts(db *sql.DB) ([]PortRecord, error) {
	rows, err := db.Query(`SELECT name, ST_Y(geom) AS lat, ST_X(geom) AS lon FROM ports WHERE geom IS NOT NULL AND name IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PortRecord
	for rows.Next() {
		var r PortRecord
		if err := rows.Scan(&r.Name, &r.Lat, &r.Lon); err != nil {
			log.Println("scan port error:", err)
			continue
		}
		// Normalize name for logging
		r.Name = strings.TrimSpace(r.Name)
		out = append(out, r)
	}
	return out, rows.Err()
}

var mu sync.Mutex
var currentSubBuffered int

func streamAIS(ctx context.Context, db *sql.DB, apiKey string, boxes [][][]float64) error {
	log.Printf("connecting to aisstream.io with %d bounding boxes...", len(boxes))

	u := "wss://stream.aisstream.io/v0/stream"
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()

	// Send subscription
	subMsg := subscribeMessage(apiKey, boxes)
	if err := c.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("write subscription: %w", err)
	}
	log.Println("subscription sent, receiving AIS data...")

	// Start a ping/pong keepalive
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				if err := c.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	c.SetReadDeadline(time.Now().Add(60 * time.Second))

	// Batch insert to reduce DB pressure
	batch := make([]AisMessage, 0, 100)
	batchTimer := time.NewTicker(5 * time.Second)
	defer batchTimer.Stop()

	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertBatch(ctx, db, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			flushBatch()
			return ctx.Err()
		case <-batchTimer.C:
			if err := flushBatch(); err != nil {
				log.Printf("batch insert error: %v", err)
			}
		default:
		}

		_, message, err := c.ReadMessage()
		if err != nil {
			flushBatch()
			return fmt.Errorf("read: %w", err)
		}

		var aisMsg AisMessage
		if err := json.Unmarshal(message, &aisMsg); err != nil {
			// Could be an error message from aisstream
			var errMsg map[string]interface{}
			if json.Unmarshal(message, &errMsg) == nil {
				if e, ok := errMsg["error"]; ok {
					log.Printf("aisstream error: %v", e)
				}
			}
			continue
		}

		if aisMsg.MessageType != "PositionReport" {
			continue
		}

		batch = append(batch, aisMsg)

		// Flush if batch is full
		if len(batch) >= 100 {
			if err := flushBatch(); err != nil {
				log.Printf("batch insert error: %v", err)
			}
		}

		mu.Lock()
		currentSubBuffered = len(batch)
		mu.Unlock()
	}
}

func insertBatch(ctx context.Context, db *sql.DB, messages []AisMessage) error {
	if len(messages) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
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
		// Parse PositionReport from the Message field
		// The Message field contains a map with key "PositionReport"
		var msgMap map[string]json.RawMessage
		if err := json.Unmarshal(msg.Message, &msgMap); err != nil {
			continue
		}
		prRaw, ok := msgMap["PositionReport"]
		if !ok {
			continue
		}

		var pr PositionReport
		if err := json.Unmarshal(prRaw, &pr); err != nil {
			continue
		}

		// Get metadata for MMSI (sometimes the PositionReport has UserID, sometimes MetaData has MMSI)
		var meta Metadata
		mmsi := pr.UserID
		if msg.MetaData != nil {
			if err := json.Unmarshal(msg.MetaData, &meta); err == nil && meta.MMSI != 0 {
				mmsi = meta.MMSI
			}
		}

		// Use position from the PositionReport
		lat := pr.Latitude
		lon := pr.Longitude

		// Use metadata coordinates as fallback
		if lat == 0 && lon == 0 && meta.Latitude != 0 {
			lat = meta.Latitude
			lon = meta.Longitude
		}

		if lat == 0 && lon == 0 {
			continue
		}

		// Full payload as JSON
		payloadBytes, _ := json.Marshal(msg)

		eventTime := time.Now()
		if pr.Timestamp > 0 && pr.Timestamp <= 60 {
			// Timestamp is seconds within the minute, use current time rounded
			now := time.Now().Truncate(time.Minute)
			eventTime = now.Add(time.Duration(pr.Timestamp) * time.Second)
		}

		_, err = stmt.ExecContext(ctx,
			mmsi,
			string(payloadBytes),
			lon, lat,
			pr.Sog,
			pr.Cog,
			eventTime,
		)
		if err != nil {
			log.Printf("insert error for mmsi %d: %v", mmsi, err)
			continue
		}
	}

	return tx.Commit()
}
