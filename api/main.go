package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/jackc/pgx/v5/stdlib"
	cache "github.com/patrickmn/go-cache"
)

type LiveEntry struct {
	ID        int64           `json:"id"`
	MMSI      *int64          `json:"mmsi,omitempty"`
	Time      time.Time       `json:"time"`
	Payload   json.RawMessage `json:"payload"`
	Position  json.RawMessage `json:"position,omitempty"`
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

	// simple in-memory cache with 30s default TTL and 1m cleanup
	c := cache.New(30*time.Second, 1*time.Minute)

	r := mux.NewRouter()

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
			  AND ST_DWithin(position, (SELECT geom FROM ports WHERE id=$1), $2)
			  AND event_time >= now() - ($3 || ' minutes')::interval
			ORDER BY event_time DESC
			LIMIT $4 OFFSET $5`

		rows, err := db.Query(q, id, float64(radius), lookback, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var out []LiveEntry
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
		rows, err := db.Query(`SELECT id, name, ST_AsGeoJSON(geom) FROM ports ORDER BY name LIMIT 1000`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		type Port struct {
			ID int64 `json:"id"`
			Name string `json:"name"`
			Geom json.RawMessage `json:"geom"`
		}
		var out []Port
		for rows.Next() {
			var p Port
			var geom sql.NullString
			if err := rows.Scan(&p.ID, &p.Name, &geom); err != nil {
				log.Println("scan ports error:", err)
				continue
			}
			if geom.Valid {
				p.Geom = json.RawMessage(geom.String)
			}
			out = append(out, p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}).Methods("GET")

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
			  AND ST_DWithin(position, (SELECT geom FROM ports WHERE id=$1), $2)
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
			Day time.Time `json:"day"`
			Count int64 `json:"count"`
		}
		var res []DayCount
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
		Addr: ":8080",
		Handler: r,
	}
	log.Println("API listening on :8080")
	log.Fatal(srv.ListenAndServe())
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
