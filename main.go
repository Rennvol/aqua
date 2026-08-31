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
	"time"
)

//go:embed static
var staticFiles embed.FS

type HealthResponse struct {
	Status string `json:"status"`
	Time   int64  `json:"time"`
	Uptime string `json:"uptime"`
}

var startTime = time.Now()

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "30221"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/api/state", handleState)

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

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
	json.NewEncoder(w).Encode(map[string]interface{}{
		"connected":   false,
		"temperature": 0,
		"voltage":     0,
		"current":     0,
		"relay_lamp":  false,
		"relay_fan":   false,
		"oled_text":   "",
		"updated_at":  time.Now().Unix(),
	})
}