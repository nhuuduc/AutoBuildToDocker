package api

import (
	"encoding/json"
	"net/http"
	"time"
)

var startTime = time.Now()

// RegisterRoutes registers health check routes on the given mux.
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/health/ready", handleReady)
	mux.HandleFunc("/health/live", handleLive)
	mux.HandleFunc("/api", handleAPIInfo)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"uptime": time.Since(startTime).String(),
	})
}

func handleReady(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func handleAPIInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "CI/CD Telegram Bot API",
		"version": "2.0.0",
		"lang":    "Go",
		"endpoints": map[string]string{
			"health": "GET /api/health",
			"ready":  "GET /health/ready",
			"live":   "GET /health/live",
		},
	})
}
