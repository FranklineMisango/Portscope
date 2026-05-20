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

    "github.com/redis/go-redis/v9"
    _ "github.com/jackc/pgx/v5/stdlib"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
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

func tryLatLonFromMap(m map[string]interface{}) (lat, lon float64, ok bool) {
    if v, okx := m["latitude"]; okx {
        switch t := v.(type) {
        case float64:
            if w, wok := m["longitude"]; wok {
                if lonf, ok2 := w.(float64); ok2 {
                    return t, lonf, true
                }
            }
        case string:
            if latf, err := strconv.ParseFloat(t, 64); err == nil {
                if w, wok := m["longitude"]; wok {
                    if lonS, ok2 := w.(string); ok2 {
                        if lonf, err2 := strconv.ParseFloat(lonS, 64); err2 == nil {
                            return latf, lonf, true
                        }
                    }
                    if lonf, ok2 := w.(float64); ok2 {
                        return latf, lonf, true
                    }
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
    if pos, ok := m["position"]; ok {
        if pm, okm := pos.(map[string]interface{}); okm {
            return tryLatLonFromMap(pm)
        }
    }
    return 0, 0, false
}

func parseMMSI(m map[string]interface{}) (int64, bool) {
    keys := []string{"mmsi", "MMSI", "Mmsi"}
    for _, k := range keys {
        if v, ok := m[k]; ok {
            switch t := v.(type) {
            case float64:
                return int64(t), true
            case int64:
                return t, true
            case int:
                return int64(t), true
            case string:
                s := strings.TrimSpace(t)
                if s == "" {
                    continue
                }
                if n, err := strconv.ParseInt(s, 10, 64); err == nil {
                    return n, true
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
    // Use Nominatim reverse geocode as a simple enrichment step.
    base := "https://nominatim.openstreetmap.org/reverse"
    u, _ := url.Parse(base)
    q := u.Query()
    q.Set("format", "jsonv2")
    q.Set("lat", fmt.Sprintf("%f", lat))
    q.Set("lon", fmt.Sprintf("%f", lon))
    u.RawQuery = q.Encode()

    req, _ := http.NewRequest("GET", u.String(), nil)
    req.Header.Set("User-Agent", "Portscope-Ingest/1.0 (+https://github.com)")

    client := &http.Client{Timeout: 5 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode != 200 {
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
        w.WriteHeader(200)
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

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    redisAddr := os.Getenv("REDIS_ADDR")
    if redisAddr == "" {
        redisAddr = "localhost:6379"
    }
    redisStream := os.Getenv("REDIS_STREAM")
    if redisStream == "" {
        redisStream = "ais:stream"
    }
    dlqStream := os.Getenv("REDIS_DLQ_STREAM")
    if dlqStream == "" {
        dlqStream = "ais:dlq"
    }
    group := os.Getenv("REDIS_GROUP")
    if group == "" {
        group = "ingest-group"
    }
    consumer := os.Getenv("REDIS_CONSUMER")
    if consumer == "" {
        consumer = fmt.Sprintf("consumer-%d", os.Getpid())
    }

    maxDeliveries := 5
    if v := os.Getenv("MAX_DELIVERIES"); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            maxDeliveries = n
        }
    }
    claimIdleMs := 60_000 // 1 minute
    if v := os.Getenv("CLAIM_IDLE_MS"); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            claimIdleMs = n
        }
    }

    pgDsn := os.Getenv("POSTGRES_DSN")
    if pgDsn == "" {
        pgDsn = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
    }

    // start metrics server
    metricsAddr := ":9090"
    if v := os.Getenv("METRICS_ADDR"); v != "" {
        metricsAddr = v
    }
    startHTTPServer(metricsAddr)

    rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
    defer rdb.Close()

    if err := ensureGroup(ctx, rdb, redisStream, group); err != nil {
        log.Fatal("ensureGroup:", err)
    }

    db, err := sql.Open("pgx", pgDsn)
    if err != nil {
        log.Fatal("pg open:", err)
    }
    defer db.Close()

    // periodic pending-claim checker
    go func() {
        tick := time.NewTicker(30 * time.Second)
        defer tick.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-tick.C:
            }
            // list pending with idle > claimIdleMs
            pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: redisStream, Group: group, Start: "-", End: "+", Count: 1000}).Result()
            if err != nil {
                if err != redis.Nil {
                    log.Println("XPendingExt error:", err)
                }
                continue
            }
            for _, p := range pending {
                if p.Idle < time.Duration(claimIdleMs)*time.Millisecond {
                    continue
                }
                // claim the message
                res, err := rdb.XClaim(ctx, &redis.XClaimArgs{Stream: redisStream, Group: group, Consumer: consumer, MinIdle: time.Duration(claimIdleMs) * time.Millisecond, Messages: []string{p.ID}}).Result()
                if err != nil {
                    log.Println("XClaim error:", err)
                    continue
                }
                if len(res) > 0 {
                    claimedCounter.Add(float64(len(res)))
                }
            }
        }
    }()

    log.Printf("consumer %s reading from stream %s (group=%s)", consumer, redisStream, group)

    for {
        select {
        case <-ctx.Done():
            log.Println("shutting down consumer")
            return
        default:
        }

        streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
            Group:    group,
            Consumer: consumer,
            Streams:  []string{redisStream, ">"},
            Count:    10,
            Block:    5 * time.Second,
        }).Result()
        if err != nil {
            if err == redis.Nil {
                continue
            }
            log.Println("XReadGroup error:", err)
            errorCounter.Inc()
            time.Sleep(1 * time.Second)
            continue
        }

        for _, str := range streams {
            for _, msg := range str.Messages {
                raw, ok := msg.Values["payload"]
                if !ok {
                    // ack and skip
                    _ = rdb.XAck(ctx, redisStream, group, msg.ID)
                    continue
                }
                payloadStr := fmt.Sprintf("%v", raw)

                var m map[string]interface{}
                if err := json.Unmarshal([]byte(payloadStr), &m); err != nil {
                    log.Println("json unmarshal payload error:", err)
                    errorCounter.Inc()
                    _, err := db.ExecContext(ctx, `INSERT INTO traffic_logs (payload) VALUES ($1)`, payloadStr)
                    if err != nil {
                        log.Println("insert raw traffic_logs error:", err)
                    }
                    _ = rdb.XAck(ctx, redisStream, group, msg.ID)
                    continue
                }

                mmsi, okm := parseMMSI(m)
                lat, lon, _ := tryLatLonFromMap(m)

                // enrichment: reverse geocode to get display_name
                var place string
                if lat != 0 || lon != 0 {
                    if name, err := reverseGeocode(lat, lon); err == nil {
                        place = name
                    }
                }

                // upsert ports table if place found (simple heuristic)
                if place != "" {
                    _, _ = db.ExecContext(ctx, `INSERT INTO ports (name, geom, last_seen) VALUES ($1, ST_SetSRID(ST_MakePoint($2,$3),4326), now()) ON CONFLICT (name) DO UPDATE SET last_seen = EXCLUDED.last_seen, geom = COALESCE(EXCLUDED.geom, ports.geom)`, place, lon, lat)
                }

                // Upsert ships
                if okm {
                    _, err := db.ExecContext(ctx, `INSERT INTO ships (mmsi, last_seen, position, payload) VALUES ($1, now(), ST_SetSRID(ST_MakePoint($2,$3),4326), $4)
                        ON CONFLICT (mmsi) DO UPDATE SET last_seen = EXCLUDED.last_seen, position = COALESCE(EXCLUDED.position, ships.position), payload = EXCLUDED.payload`, mmsi, lon, lat, payloadStr)
                    if err != nil {
                        log.Println("upsert ship error:", err)
                        errorCounter.Inc()
                    }
                }

                // parse other fields
                var dest string
                if d, ok := m["destination"]; ok {
                    dest = fmt.Sprintf("%v", d)
                } else if d, ok := m["Destination"]; ok {
                    dest = fmt.Sprintf("%v", d)
                }
                var speed, course sql.NullFloat64
                if s, ok := m["speed"]; ok {
                    switch t := s.(type) {
                    case float64:
                        speed = sql.NullFloat64{Float64: t, Valid: true}
                    case string:
                        if v, err := strconv.ParseFloat(t, 64); err == nil {
                            speed = sql.NullFloat64{Float64: v, Valid: true}
                        }
                    }
                }
                if c, ok := m["course"]; ok {
                    switch t := c.(type) {
                    case float64:
                        course = sql.NullFloat64{Float64: t, Valid: true}
                    case string:
                        if v, err := strconv.ParseFloat(t, 64); err == nil {
                            course = sql.NullFloat64{Float64: v, Valid: true}
                        }
                    }
                }

                // insert into traffic_logs
                var errInsert error
                if lat != 0 || lon != 0 {
                    _, errInsert = db.ExecContext(ctx, `INSERT INTO traffic_logs (mmsi, payload, position, speed_kts, course_deg, destination) VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3,$4),4326), $5, $6, $7)`,
                        sql.NullInt64{Int64: mmsi, Valid: okm}, payloadStr, lon, lat, speed, course, dest)
                } else {
                    _, errInsert = db.ExecContext(ctx, `INSERT INTO traffic_logs (mmsi, payload, speed_kts, course_deg, destination) VALUES ($1, $2, $3, $4, $5)`,
                        sql.NullInt64{Int64: mmsi, Valid: okm}, payloadStr, speed, course, dest)
                }
                if errInsert != nil {
                    log.Println("insert traffic_logs error:", errInsert)
                    errorCounter.Inc()
                }

                // check delivery count; if too many deliveries, move to DLQ
                pendingInfo, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: redisStream, Group: group, Start: msg.ID, End: msg.ID, Count: 1}).Result()
                if err == nil && len(pendingInfo) > 0 {
                    if int(pendingInfo[0].RetryCount) > maxDeliveries {
                        // send to DLQ
                        _ = rdb.XAdd(ctx, &redis.XAddArgs{Stream: dlqStream, Values: map[string]interface{}{"orig_id": msg.ID, "payload": payloadStr}}).Err()
                        _ = rdb.XAck(ctx, redisStream, group, msg.ID)
                        dlqCounter.Inc()
                        continue
                    }
                }

                // ack the message
                if err := rdb.XAck(ctx, redisStream, group, msg.ID).Err(); err != nil {
                    log.Println("XAck error:", err)
                    errorCounter.Inc()
                } else {
                    processedCounter.Inc()
                }
            }
        }
    }
}
