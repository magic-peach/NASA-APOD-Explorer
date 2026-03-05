package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	Payload []byte
	Expiry  time.Time
}

var (
	apiKey = envOrDefault("NASA_API_KEY", "DEMO_KEY")
	client = http.Client{Timeout: 10 * time.Second}
	cache  = struct {
		sync.RWMutex
		items map[string]cacheEntry
	}{items: map[string]cacheEntry{}}
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apod", apodHandler)
	mux.HandleFunc("/api/space-facts", spaceFactsHandler)
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	addr := ":8080"
	log.Printf("NASA APOD Explorer running on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Fatal(err)
	}
}

func apodHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := r.URL.Query().Get("start_date")
	end := r.URL.Query().Get("end_date")
	date := r.URL.Query().Get("date")
	count := r.URL.Query().Get("count")

	url := "https://api.nasa.gov/planetary/apod?api_key=" + apiKey
	if start != "" && end != "" {
		url += "&start_date=" + start + "&end_date=" + end
	} else if date != "" {
		url += "&date=" + date
	} else if count != "" {
		url += "&count=" + count
	}

	payload, err := getOrFetch(url)
	if err != nil {
		http.Error(w, "failed to fetch APOD", http.StatusBadGateway)
		log.Printf("APOD fetch failed: %v", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(payload)
}

func spaceFactsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload, err := getOrFetch("https://api.le-systeme-solaire.net/rest/bodies/")
	if err != nil {
		http.Error(w, "failed to fetch space facts", http.StatusBadGateway)
		log.Printf("Space facts fetch failed: %v", err)
		return
	}

	var body struct {
		Bodies []map[string]any `json:"bodies"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		http.Error(w, "failed to parse space facts", http.StatusBadGateway)
		return
	}

	var planets, moons int
	var totalGravity, totalRadius float64
	var countedGravity, countedRadius int

	for _, b := range body.Bodies {
		if strings.EqualFold(fmt.Sprint(b["bodyType"]), "Planet") {
			planets++
		}
		if v, ok := b["moons"]; ok && v != nil {
			if arr, ok := v.([]any); ok {
				moons += len(arr)
			}
		}
		if g, ok := asFloat64(b["gravity"]); ok {
			totalGravity += g
			countedGravity++
		}
		if r, ok := asFloat64(b["meanRadius"]); ok {
			totalRadius += r
			countedRadius++
		}
	}

	resp := map[string]any{
		"planets":     planets,
		"moons":       moons,
		"avg_gravity": round2(safeDivide(totalGravity, float64(countedGravity))),
		"avg_radius":  round2(safeDivide(totalRadius, float64(countedRadius))),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func getOrFetch(url string) ([]byte, error) {
	cache.RLock()
	entry, ok := cache.items[url]
	cache.RUnlock()
	if ok && time.Now().Before(entry.Expiry) {
		return entry.Payload, nil
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	cache.Lock()
	cache.items[url] = cacheEntry{Payload: payload, Expiry: time.Now().Add(10 * time.Minute)}
	cache.Unlock()

	return payload, nil
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func asFloat64(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok
}

func safeDivide(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
