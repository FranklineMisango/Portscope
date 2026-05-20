package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func tryLatLon(m map[string]interface{}) (lat, lon float64, ok bool) {
	// common keys
	if v, okx := m["latitude"]; okx {
		if latf, okf := v.(float64); okf {
			if w, wok := m["longitude"]; wok {
				if lonf, ok2 := w.(float64); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	if v, okx := m["lat"]; okx {
		if latf, okf := v.(float64); okf {
			if w, wok := m["lon"]; wok {
				if lonf, ok2 := w.(float64); ok2 {
					return latf, lonf, true
				}
			}
		}
	}
	// nested position
	if pos, ok := m["position"]; ok {
		if pm, okm := pos.(map[string]interface{}); okm {
			return tryLatLon(pm)
		}
	}
	return 0, 0, false
}

func main() {
	apiKey := os.Getenv("AISSTREAM_API_KEY")
	if apiKey == "" {
		log.Fatal("AISSTREAM_API_KEY not set. See .env.example")
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisStream := os.Getenv("REDIS_STREAM")
	if redisStream == "" {
		redisStream = "ais:stream"
	}

	pgDsn := os.Getenv("POSTGRES_DSN")
	if pgDsn == "" {
		pgDsn = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}

	u := url.URL{Scheme: "wss", Host: "stream.aisstream.io", Path: "/v0/stream"}
	log.Printf("connecting to %s", u.String())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// redis client
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()

	// postgres
	db, err := sql.Open("pgx", pgDsn)
	if err != nil {
		log.Fatal("postgres open:", err)
	}
	defer db.Close()

	// dial websocket
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer c.Close()

	sub := map[string]interface{}{
		"Apikey": apiKey,
		"BoundingBoxes": [][]float64{{-4.05, 39.65}, {-3.95, 39.75}},
		"FilterMessageTypes": []string{"PositionReport"},
	}

	if err := c.WriteJSON(sub); err != nil {
		log.Fatal("write subscription:", err)
	}

	log.Println("subscription sent, publishing to Redis stream and Postgres...")

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read error:", err)
				return
			}

			// publish to redis stream
			vals := map[string]interface{}{"payload": string(message)}
			if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: redisStream, Values: vals}).Err(); err != nil {
				log.Println("redis XAdd error:", err)
			}

			// try parse and insert into postgres
			var m map[string]interface{}
			if err := json.Unmarshal(message, &m); err == nil {
				lat, lon, ok := tryLatLon(m)
				if ok {
					// insert with geometry
					_, err = db.ExecContext(ctx, `INSERT INTO ais_messages (payload, position) VALUES ($1, ST_SetSRID(ST_MakePoint($2, $3), 4326))`, message, lon, lat)
				} else {
					_, err = db.ExecContext(ctx, `INSERT INTO ais_messages (payload) VALUES ($1)`, message)
				}
				if err != nil {
					log.Println("postgres insert error:", err)
				}
			} else {
				// fallback: store raw
				_, err = db.ExecContext(ctx, `INSERT INTO ais_messages (payload) VALUES ($1)`, message)
				if err != nil {
					log.Println("postgres insert fallback error:", err)
				}
			}

			// small backoff to avoid hot loop if needed
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("interrupt, shutting down")
		c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
		}
	}
}
