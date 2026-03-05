package main

import (
	"bytes"
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

type llmRequest struct {
	Task        string `json:"task"`
	Level       string `json:"level"`
	Title       string `json:"title"`
	Date        string `json:"date"`
	Explanation string `json:"explanation"`
	Question    string `json:"question"`
	Distance    string `json:"distance"`
}

var (
	apiKey     = envOrDefault("NASA_API_KEY", "DEMO_KEY")
	llmAPIKey  = os.Getenv("OPENAI_API_KEY")
	llmModel   = envOrDefault("LLM_MODEL", "gpt-4o-mini")
	llmBaseURL = envOrDefault("LLM_BASE_URL", "https://api.openai.com/v1/chat/completions")
	client     = http.Client{Timeout: 15 * time.Second}
	cache      = struct {
		sync.RWMutex
		items map[string]cacheEntry
	}{items: map[string]cacheEntry{}}
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apod", apodHandler)
	mux.HandleFunc("/api/space-facts", spaceFactsHandler)
	mux.HandleFunc("/api/llm", llmHandler)
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

func llmHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req llmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Task) == "" {
		http.Error(w, "task is required", http.StatusBadRequest)
		return
	}

	response, raw, err := runLLMTask(req)
	if err != nil {
		log.Printf("LLM task failed: %v", err)
		response = fallbackLLM(req)
	}

	resp := map[string]any{
		"response": response,
		"source":   "fallback",
	}
	if raw {
		resp["source"] = "llm"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func runLLMTask(req llmRequest) (string, bool, error) {
	if llmAPIKey == "" {
		return "", false, fmt.Errorf("OPENAI_API_KEY not configured")
	}

	systemPrompt := "You are an astronomy teaching assistant for NASA APOD images. Keep responses accurate, concise, and user-friendly."
	userPrompt := buildTaskPrompt(req)
	if userPrompt == "" {
		return "", false, fmt.Errorf("unsupported task")
	}

	payload := map[string]any{
		"model": llmModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.6,
	}

	body, _ := json.Marshal(payload)
	httpReq, _ := http.NewRequest(http.MethodPost, llmBaseURL, bytes.NewBuffer(body))
	httpReq.Header.Set("Authorization", "Bearer "+llmAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("llm status %d: %s", resp.StatusCode, string(msg))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", false, err
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("llm returned no choices")
	}

	return strings.TrimSpace(parsed.Choices[0].Message.Content), true, nil
}

func buildTaskPrompt(req llmRequest) string {
	base := fmt.Sprintf("Image Title: %s\nDate: %s\nNASA Explanation: %s\n", req.Title, req.Date, req.Explanation)
	switch req.Task {
	case "rewrite":
		level := req.Level
		if level == "" {
			level = "Beginner"
		}
		return base + fmt.Sprintf("Rewrite the explanation for a %s audience. Keep it under 140 words.", level)
	case "chat":
		return base + fmt.Sprintf("Answer this user question in a teaching tone: %s", req.Question)
	case "hotspots":
		return base + "Identify up to 4 visible objects. Return plain text bullet list with label + short description + approximate position (top-left, center, etc)."
	case "facts":
		return base + "Return 3 'Did you know?' bullet facts relevant to this image."
	case "similar":
		return base + "Suggest 3 similar APOD-worthy space objects/images and why in short bullets."
	case "timeline":
		return base + "Explain why this date/image fits in astronomy history in 2-3 sentences."
	case "eli10":
		return base + "Explain this image like the user is 10 years old in 2-4 simple sentences."
	case "distance":
		return base + fmt.Sprintf("Turn this distance into relatable comparisons: %s. If missing, estimate from context and note uncertainty.", req.Distance)
	case "quiz":
		return base + "Create 1 multiple-choice quiz question with 4 options and then provide answer + short explanation."
	case "story":
		return base + "Write a short, engaging story-mode narrative (max 160 words) about the science in this image."
	case "lesson":
		return base + "Create a mini lesson with 3 sections: Concept, How it works, Why it matters. Keep concise."
	case "captions":
		return base + "Generate 3 captions: scientific, poetic, and fun."
	case "collections":
		return base + "Classify this image into one collection (Black Holes, Nebulae, Galaxies, Supernovae, Planets, Other) and suggest 3 related collection topics."
	default:
		return ""
	}
}

func fallbackLLM(req llmRequest) string {
	summary := strings.TrimSpace(req.Explanation)
	if len(summary) > 220 {
		summary = summary[:220] + "..."
	}
	switch req.Task {
	case "rewrite":
		if strings.EqualFold(req.Level, "Expert") {
			return "Expert view: " + summary
		}
		if strings.EqualFold(req.Level, "Student") {
			return "Student view: " + summary
		}
		return "Beginner view: This space image shows important cosmic structures. " + summary
	case "chat":
		return fmt.Sprintf("Based on NASA's description, here's a guided answer: %s\nQuestion: %s", summary, req.Question)
	case "hotspots":
		return "• Bright core (center): likely the main focus object\n• Gas and dust lanes (around center): structured clouds\n• Stellar points (across frame): foreground/background stars"
	case "facts":
		return "• APOD entries are selected daily by NASA astronomers.\n• Many APOD colors are mapped to scientific filters, not human-eye colors.\n• Light from deep-space objects can take thousands to millions of years to reach us."
	case "similar":
		return "• Orion Nebula — star-forming gas clouds\n• Eagle Nebula — dramatic pillars and newborn stars\n• Carina Nebula — massive stellar nursery"
	case "timeline":
		return "This APOD reflects how modern astronomy combines space telescopes and multi-wavelength imaging to reveal structure and history of the universe."
	case "eli10":
		return "Imagine a giant glowing cloud in space where stars are born. The bright colors show different gases lighting up like neon signs."
	case "distance":
		return "At cosmic distances, even light needs years to travel. If you flew there with today's planes, the trip would take far longer than human civilization has existed."
	case "quiz":
		return "What type of object is highlighted most here?\nA) Comet\nB) Nebula\nC) Asteroid\nD) Moon\nAnswer: B — Nebula. These glowing gas clouds are common APOD subjects."
	case "story":
		return "Long before humans looked up, this region in space was already shaping stars. Gas drifted, gravity gathered it, and light slowly emerged — a cosmic story captured in one frame."
	case "lesson":
		return "Concept: Space images show physical processes like gravity and radiation.\nHow it works: Telescopes collect light in different wavelengths.\nWhy it matters: We learn how stars, planets, and galaxies evolve."
	case "captions":
		return "Scientific: Emission structures reveal energetic astrophysical processes.\nPoetic: A cathedral of starlight painted across the dark.\nFun: Space really said, 'let me show off tonight.'"
	case "collections":
		return "Collection: Other\nRelated: Nebulae, Galaxies, Star Clusters"
	default:
		return "AI helper is currently unavailable."
	}
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
