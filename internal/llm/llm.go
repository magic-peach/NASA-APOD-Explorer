// Package llm powers the "explain this image" helpers. It calls an
// OpenAI-compatible chat-completions endpoint when an API key is configured,
// and otherwise (or on any error) returns canned offline text so the UI never
// breaks.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Request is what the browser sends for one helper task.
type Request struct {
	Task        string `json:"task"`
	Level       string `json:"level"`
	Title       string `json:"title"`
	Date        string `json:"date"`
	Explanation string `json:"explanation"`
	Question    string `json:"question"`
	Distance    string `json:"distance"`
}

// Response is the helper's answer and where it came from.
type Response struct {
	Text   string `json:"response"`
	Source string `json:"source"` // "llm" or "fallback"
}

// Assistant answers helper requests.
type Assistant struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	Logger     *slog.Logger // optional; defaults to slog.Default()
}

// Run answers req and never fails: if no model is configured, the task is
// unknown, or the remote call errors, it returns offline fallback text with
// Source "fallback" so the UI always has something to show.
func (a *Assistant) Run(ctx context.Context, req Request) Response {
	prompt := buildTaskPrompt(req)
	if prompt != "" && a.APIKey != "" {
		text, err := a.complete(ctx, prompt)
		if err == nil {
			return Response{Text: text, Source: "llm"}
		}
		a.logger().Warn("llm request failed, using fallback", "task", req.Task, "err", err)
	}
	return Response{Text: fallback(req), Source: "fallback"}
}

func (a *Assistant) logger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

const systemPrompt = "You are an astronomy teaching assistant for NASA APOD images. Keep responses accurate, concise, and user-friendly."

// complete sends one chat-completions request and returns the reply text.
func (a *Assistant) complete(ctx context.Context, userPrompt string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model": a.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.6,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return "", fmt.Errorf("llm: status %d: %s", resp.StatusCode, msg)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("llm: no choices returned")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// buildTaskPrompt returns the user prompt for a task, or "" if unknown.
func buildTaskPrompt(req Request) string {
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

// fallback returns offline text for a task when no model is reachable.
func fallback(req Request) string {
	summary := strings.TrimSpace(req.Explanation)
	if r := []rune(summary); len(r) > 220 {
		summary = string(r[:220]) + "..." // slice runes, not bytes, to keep UTF-8 valid
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
