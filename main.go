package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

//go:embed static
var staticFiles embed.FS

var startTime = time.Now()

func init() {
	initLog()
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "30221"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/relay", handleRelay)
	mux.HandleFunc("/api/oled", handleOLED)
	mux.HandleFunc("/api/logs", handleLogs)
	mux.HandleFunc("/api/settings", handleSetSettings)
	mux.HandleFunc("/api/settings/get", handleGetSettings)

	go RunSerial()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", accessLogger(http.FileServer(http.FS(sub))))

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		log.Println("shutting down...")
		server.Close()
	}()

	fmt.Printf("🌊 aqua — :%s\n", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func realIP(r *http.Request) string {
	xf := r.Header.Get("X-Forwarded-For")
	if xf != "" {
		parts := strings.Split(xf, ",")
		return strings.TrimSpace(parts[0])
	}
	// strip port
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Status: "ok",
		Time:   time.Now().Unix(),
		Uptime: time.Since(startTime).Round(time.Second).String(),
	})
}

func handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state.snapshot())
}

type HealthResponse struct {
	Status string `json:"status"`
	Time   int64  `json:"time"`
	Uptime string `json:"uptime"`
}

/* ── relay ── */

func handleRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		Relay string `json:"relay"`
		On    bool   `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	if req.Relay != "lamp" && req.Relay != "fan" {
		json.NewEncoder(w).Encode(map[string]string{"error": "relay must be 'lamp' or 'fan'"})
		return
	}

	state.setRelay(req.Relay, req.On)

	label := "lampu"
	if req.Relay == "fan" {
		label = "kipas"
	}
	what := "ON"
	if !req.On {
		what = "OFF"
	}
	addLog(realIP(r), fmt.Sprintf("relay %s: %s", label, what))

	// TODO: send command to UNO via serial
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"relay":  req.Relay,
		"on":     req.On,
	})
}

/* ── oled ── */

func handleOLED(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	if len(req.Text) > 128 {
		req.Text = req.Text[:128]
	}

	state.mu.Lock()
	state.OLEDText = req.Text
	state.mu.Unlock()

	addLog(realIP(r), fmt.Sprintf("oled: %s", req.Text))

	// TODO: send to UNO via serial
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"text":   req.Text,
		"sent":   true,
	})
}