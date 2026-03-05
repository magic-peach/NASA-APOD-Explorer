package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	Payload []byte
	Expiry  time.Time
}

type apodItem struct {
	Date        string `json:"date"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	URL         string `json:"url"`
	MediaType   string `json:"media_type"`
	HDURL       string `json:"hdurl"`
}

var (
	apiKey = envOrDefault("NASA_API_KEY", "DEMO_KEY")
	client = http.Client{Timeout: 10 * time.Second}
	cache  = struct {
		sync.RWMutex
		items map[string]cacheEntry
	}{items: map[string]cacheEntry{}}
	sessions = struct {
		sync.RWMutex
		items map[string]int64
	}{items: map[string]int64{}}
)

func main() {
	if err := initSchema(); err != nil {
		log.Fatalf("failed to initialize schema: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/signup", signupHandler)
	mux.HandleFunc("/api/login", loginHandler)
	mux.HandleFunc("/api/logout", logoutHandler)
	mux.HandleFunc("/api/apod", apodHandler)
	mux.HandleFunc("/api/space-facts", spaceFactsHandler)
	mux.HandleFunc("/api/favorites", favoritesHandler)
	mux.HandleFunc("/api/history", historyHandler)
	mux.HandleFunc("/api/comments", commentsHandler)
	mux.HandleFunc("/api/ratings", ratingsHandler)
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	addr := ":8080"
	log.Printf("NASA APOD Explorer running on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, withCORS(mux)))
}

func initSchema() error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT UNIQUE NOT NULL, email TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS favorites (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, apod_date TEXT NOT NULL, image_url TEXT, title TEXT, saved_at DATETIME DEFAULT CURRENT_TIMESTAMP, UNIQUE(user_id, apod_date));`,
		`CREATE TABLE IF NOT EXISTS history (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, apod_date TEXT NOT NULL, title TEXT, viewed_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS apod_cache (date TEXT PRIMARY KEY, title TEXT, explanation TEXT, image_url TEXT, media_type TEXT, hd_url TEXT, stored_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS ai_questions (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, image_date TEXT, question TEXT, answer TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS comments (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, image_date TEXT, comment_text TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS ratings (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, image_date TEXT, rating INTEGER CHECK(rating BETWEEN 1 AND 5), UNIQUE(user_id, image_date));`,
		`CREATE TABLE IF NOT EXISTS collections (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, name TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS collection_images (collection_id INTEGER NOT NULL, image_date TEXT NOT NULL, PRIMARY KEY(collection_id, image_date));`,
		`CREATE TABLE IF NOT EXISTS user_preferences (user_id INTEGER PRIMARY KEY, preferred_keyword TEXT, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS image_metadata (date TEXT PRIMARY KEY, object_type TEXT, distance_light_years REAL, telescope TEXT, discovery_year INTEGER, category TEXT);`,
		`CREATE TABLE IF NOT EXISTS ai_explanations (image_date TEXT PRIMARY KEY, beginner_explanation TEXT, expert_explanation TEXT, fun_facts TEXT, generated_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS quiz_results (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, image_date TEXT, score INTEGER, answered_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS admin_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, action TEXT, actor_user_id INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS image_stats (image_date TEXT PRIMARY KEY, views INTEGER DEFAULT 0, likes INTEGER DEFAULT 0, comments INTEGER DEFAULT 0);`,
	}
	for _, stmt := range schema {
		if _, err := sqlExec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func signupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Username, Email, Password string }
	if !decodeJSON(r, &in) || in.Username == "" || in.Email == "" || len(in.Password) < 6 {
		http.Error(w, "invalid signup payload", http.StatusBadRequest)
		return
	}
	hash := hashPassword(in.Password)
	_, err := sqlExec(fmt.Sprintf("INSERT INTO users(username,email,password_hash) VALUES(%s,%s,%s)", s(in.Username), s(in.Email), s(hash)))
	if err != nil {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	}
	jsonResponse(w, map[string]any{"ok": true})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Username, Password string }
	if !decodeJSON(r, &in) {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	rows, _ := sqlQuery(fmt.Sprintf("SELECT id,password_hash FROM users WHERE username=%s OR email=%s LIMIT 1", s(in.Username), s(in.Username)))
	if len(rows) == 0 {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	if fmt.Sprint(rows[0]["password_hash"]) != hashPassword(in.Password) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	uid := int64(rows[0]["id"].(float64))
	token := randomToken()
	sessions.Lock()
	sessions.items[token] = uid
	sessions.Unlock()
	jsonResponse(w, map[string]any{"token": token})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	sessions.Lock()
	delete(sessions.items, token)
	sessions.Unlock()
	jsonResponse(w, map[string]any{"ok": true})
}

func apodHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uid, _ := optionalAuth(r)
	start, end := r.URL.Query().Get("start_date"), r.URL.Query().Get("end_date")
	date, count := r.URL.Query().Get("date"), r.URL.Query().Get("count")
	if date != "" {
		rows, _ := sqlQuery(fmt.Sprintf("SELECT date,title,explanation,image_url,media_type,hd_url FROM apod_cache WHERE date=%s", s(date)))
		if len(rows) > 0 {
			item := map[string]any{"date": rows[0]["date"], "title": rows[0]["title"], "explanation": rows[0]["explanation"], "url": rows[0]["image_url"], "media_type": rows[0]["media_type"], "hdurl": rows[0]["hd_url"]}
			if uid > 0 {
				trackHistory(uid, fmt.Sprint(rows[0]["date"]), fmt.Sprint(rows[0]["title"]))
			}
			jsonResponse(w, item)
			return
		}
	}
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
		return
	}
	storeAPODPayload(payload)
	if uid > 0 {
		trackHistoryFromPayload(uid, payload)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(payload)
}

func favoritesHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireAuth(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		rows, _ := sqlQuery(fmt.Sprintf("SELECT id,apod_date as date,image_url as url,title,saved_at FROM favorites WHERE user_id=%d ORDER BY saved_at DESC", uid))
		jsonResponse(w, rows)
		return
	}
	if r.Method == http.MethodPost {
		var in struct{ Date, URL, Title string }
		if !decodeJSON(r, &in) {
			http.Error(w, "bad payload", 400)
			return
		}
		q := fmt.Sprintf("INSERT INTO favorites(user_id,apod_date,image_url,title) VALUES(%d,%s,%s,%s) ON CONFLICT(user_id,apod_date) DO UPDATE SET image_url=excluded.image_url,title=excluded.title,saved_at=CURRENT_TIMESTAMP", uid, s(in.Date), s(in.URL), s(in.Title))
		_, _ = sqlExec(q)
		_, _ = sqlExec(fmt.Sprintf("INSERT INTO image_stats(image_date,likes) VALUES(%s,1) ON CONFLICT(image_date) DO UPDATE SET likes=likes+1", s(in.Date)))
		jsonResponse(w, map[string]any{"ok": true})
		return
	}
	http.Error(w, "method not allowed", 405)
}

func historyHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := requireAuth(w, r)
	if !ok {
		return
	}
	rows, _ := sqlQuery(fmt.Sprintf("SELECT apod_date as date,title,viewed_at FROM history WHERE user_id=%d ORDER BY viewed_at DESC LIMIT 30", uid))
	jsonResponse(w, rows)
}

func commentsHandler(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date", 400)
		return
	}
	if r.Method == http.MethodGet {
		rows, _ := sqlQuery(fmt.Sprintf("SELECT u.username, c.comment_text as comment, c.created_at FROM comments c JOIN users u ON u.id=c.user_id WHERE image_date=%s ORDER BY c.created_at DESC", s(date)))
		jsonResponse(w, rows)
		return
	}
	uid, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if !decodeJSON(r, &in) || strings.TrimSpace(in.Comment) == "" {
		http.Error(w, "bad payload", 400)
		return
	}
	_, _ = sqlExec(fmt.Sprintf("INSERT INTO comments(user_id,image_date,comment_text) VALUES(%d,%s,%s)", uid, s(date), s(in.Comment)))
	_, _ = sqlExec(fmt.Sprintf("INSERT INTO image_stats(image_date,comments) VALUES(%s,1) ON CONFLICT(image_date) DO UPDATE SET comments=comments+1", s(date)))
	jsonResponse(w, map[string]any{"ok": true})
}

func ratingsHandler(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "missing date", 400)
		return
	}
	if r.Method == http.MethodGet {
		rows, _ := sqlQuery(fmt.Sprintf("SELECT COALESCE(AVG(rating),0) as average FROM ratings WHERE image_date=%s", s(date)))
		avg := 0.0
		if len(rows) > 0 {
			avg = rows[0]["average"].(float64)
		}
		jsonResponse(w, map[string]any{"average": math.Round(avg*100) / 100})
		return
	}
	uid, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var in struct {
		Rating int `json:"rating"`
	}
	if !decodeJSON(r, &in) || in.Rating < 1 || in.Rating > 5 {
		http.Error(w, "invalid rating", 400)
		return
	}
	_, _ = sqlExec(fmt.Sprintf("INSERT INTO ratings(user_id,image_date,rating) VALUES(%d,%s,%d) ON CONFLICT(user_id,image_date) DO UPDATE SET rating=excluded.rating", uid, s(date), in.Rating))
	jsonResponse(w, map[string]any{"ok": true})
}

func trackHistory(uid int64, date, title string) {
	_, _ = sqlExec(fmt.Sprintf("INSERT INTO history(user_id,apod_date,title) VALUES(%d,%s,%s)", uid, s(date), s(title)))
	_, _ = sqlExec(fmt.Sprintf("INSERT INTO image_stats(image_date,views) VALUES(%s,1) ON CONFLICT(image_date) DO UPDATE SET views=views+1", s(date)))
}

func trackHistoryFromPayload(uid int64, payload []byte) {
	var one apodItem
	if json.Unmarshal(payload, &one) == nil && one.Date != "" {
		trackHistory(uid, one.Date, one.Title)
		return
	}
	var many []apodItem
	if json.Unmarshal(payload, &many) != nil {
		return
	}
	for _, item := range many {
		trackHistory(uid, item.Date, item.Title)
	}
}

func storeAPODPayload(payload []byte) {
	var one apodItem
	if json.Unmarshal(payload, &one) == nil && one.Date != "" {
		storeAPOD(one)
		return
	}
	var many []apodItem
	if json.Unmarshal(payload, &many) != nil {
		return
	}
	for _, item := range many {
		storeAPOD(item)
	}
}

func storeAPOD(a apodItem) {
	q := fmt.Sprintf("INSERT INTO apod_cache(date,title,explanation,image_url,media_type,hd_url,stored_at) VALUES(%s,%s,%s,%s,%s,%s,CURRENT_TIMESTAMP) ON CONFLICT(date) DO UPDATE SET title=excluded.title, explanation=excluded.explanation, image_url=excluded.image_url, media_type=excluded.media_type, hd_url=excluded.hd_url, stored_at=CURRENT_TIMESTAMP", s(a.Date), s(a.Title), s(a.Explanation), s(a.URL), s(a.MediaType), s(a.HDURL))
	_, _ = sqlExec(q)
}

func spaceFactsHandler(w http.ResponseWriter, r *http.Request) {
	payload, err := getOrFetch("https://api.le-systeme-solaire.net/rest/bodies/")
	if err != nil {
		http.Error(w, "failed to fetch space facts", 502)
		return
	}
	var body struct {
		Bodies []map[string]any `json:"bodies"`
	}
	if json.Unmarshal(payload, &body) != nil {
		http.Error(w, "failed to parse", 502)
		return
	}
	var planets, moons int
	var tg, tr float64
	var cg, cr int
	for _, b := range body.Bodies {
		if strings.EqualFold(fmt.Sprint(b["bodyType"]), "Planet") {
			planets++
		}
		if v, ok := b["moons"].([]any); ok {
			moons += len(v)
		}
		if g, ok := asFloat64(b["gravity"]); ok {
			tg += g
			cg++
		}
		if rr, ok := asFloat64(b["meanRadius"]); ok {
			tr += rr
			cr++
		}
	}
	jsonResponse(w, map[string]any{"planets": planets, "moons": moons, "avg_gravity": round2(safeDivide(tg, float64(cg))), "avg_radius": round2(safeDivide(tr, float64(cr)))})
}

func sqlExec(query string) (string, error) {
	cmd := exec.Command("sqlite3", "app.db", query)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func sqlQuery(query string) ([]map[string]any, error) {
	cmd := exec.Command("sqlite3", "-json", "app.db", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []map[string]any{}, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func s(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func hashPassword(password string) string {
	salt := envOrDefault("APP_SALT", "nasa-apod-salt")
	s := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(s[:])
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAuth(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, ok := optionalAuth(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return 0, false
	}
	return uid, true
}

func optionalAuth(r *http.Request) (int64, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	sessions.RLock()
	uid, ok := sessions.items[token]
	sessions.RUnlock()
	return uid, ok
}

func randomToken() string { b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func decodeJSON(r *http.Request, out any) bool {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out) == nil
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func asFloat64(v any) (float64, bool) { n, ok := v.(float64); return n, ok }

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

func safeDivide(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
