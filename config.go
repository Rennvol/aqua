package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
)

type Settings struct {
	mu             sync.RWMutex
	OLEDLine1      string `json:"oled_line1"`      // what to show on OLED line 1
	OLEDLine2      string `json:"oled_line2"`       // line 2
	OLEDLine3      string `json:"oled_line3"`       // line 3
	OLEDLine4      string `json:"oled_line4"`       // line 4
	PollInterval   int    `json:"poll_interval"`     // seconds
}

var settingsPath = "config.json"

func defaultSettings() *Settings {
	return &Settings{
		OLEDLine1:    "temp",
		OLEDLine2:    "current",
		OLEDLine3:    "voltage",
		OLEDLine4:    "relay",
		PollInterval: 5,
	}
}

var settings = loadSettings()

func loadSettings() *Settings {
	s := defaultSettings()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return s
	}
	json.Unmarshal(data, s)
	return s
}

func saveSettings(s *Settings) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(settingsPath, b, 0644)
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"oled_line1":    settings.OLEDLine1,
		"oled_line2":    settings.OLEDLine2,
		"oled_line3":    settings.OLEDLine3,
		"oled_line4":    settings.OLEDLine4,
		"poll_interval": settings.PollInterval,
	})
}

func handleSetSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"POST required"}`, 405)
		return
	}
	var req struct {
		OLEDLine1    string `json:"oled_line1"`
		OLEDLine2    string `json:"oled_line2"`
		OLEDLine3    string `json:"oled_line3"`
		OLEDLine4    string `json:"oled_line4"`
		PollInterval int    `json:"poll_interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	settings.mu.Lock()
	if req.OLEDLine1 != "" {
		settings.OLEDLine1 = req.OLEDLine1
	}
	if req.OLEDLine2 != "" {
		settings.OLEDLine2 = req.OLEDLine2
	}
	if req.OLEDLine3 != "" {
		settings.OLEDLine3 = req.OLEDLine3
	}
	if req.OLEDLine4 != "" {
		settings.OLEDLine4 = req.OLEDLine4
	}
	if req.PollInterval > 0 {
		settings.PollInterval = req.PollInterval
	}
	settings.mu.Unlock()
	saveSettings(settings)
	addLog(realIP(r), "settings: update")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}