package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
)

type RelayDef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Icon  string `json:"icon"`
}

type SensorDef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
	Icon  string `json:"icon"`
}

type Settings struct {
	mu           sync.RWMutex
	Pin          string      `json:"pin"`
	OLEDLine1    string      `json:"oled_line1"`
	OLEDLine2    string      `json:"oled_line2"`
	OLEDLine3    string      `json:"oled_line3"`
	OLEDLine4    string      `json:"oled_line4"`
	PollInterval int         `json:"poll_interval"`
	CronAPIKey   string      `json:"cron_api_key"`
	CronToken    string      `json:"cron_token"`
	PublicURL    string      `json:"public_url"`
	Schedules    []Schedule  `json:"schedules"`
	Relays       []RelayDef  `json:"relays"`
	Sensors      []SensorDef `json:"sensors"`
}

var settingsPath = "config.json"

func defaultSettings() *Settings {
	return &Settings{
		Pin:          "1234",
		OLEDLine1:    "temp",
		OLEDLine2:    "current",
		OLEDLine3:    "voltage",
		OLEDLine4:    "relay",
		PollInterval: 5,
		Relays: []RelayDef{
			{ID: "lamp", Label: "Lampu", Icon: "💡"},
			{ID: "fan", Label: "Kipas", Icon: "🌬️"},
		},
		Sensors: []SensorDef{
			{ID: "temp", Label: "Suhu Air", Unit: "°C", Icon: "🌡️"},
			{ID: "voltage", Label: "Tegangan", Unit: "V", Icon: "⚡"},
			{ID: "current", Label: "Arus", Unit: "A", Icon: "🔌"},
			{ID: "power", Label: "Daya", Unit: "W", Icon: "🔋"},
		},
	}
}

var settings = loadSettings()

func loadSettings() *Settings {
	s := defaultSettings()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return s
	}
	// merge json onto defaults (keep defaults if missing)
	tmp := *s
	if err := json.Unmarshal(data, &tmp); err == nil {
		// backfill empty relays/sensors
		if len(tmp.Relays) == 0 {
			tmp.Relays = s.Relays
		}
		if len(tmp.Sensors) == 0 {
			tmp.Sensors = s.Sensors
		}
		// ensure pin etc not empty
		if tmp.Pin == "" {
			tmp.Pin = s.Pin
		}
		if tmp.OLEDLine1 == "" {
			tmp.OLEDLine1 = s.OLEDLine1
		}
		if tmp.PollInterval == 0 {
			tmp.PollInterval = s.PollInterval
		}
		*s = tmp
	}
	return s
}

func saveSettings(s *Settings) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(settingsPath, b, 0644)
	if db != nil {
		dbPersistAll()
	}
}

func saveSettingsLocked() {
	b, _ := json.MarshalIndent(settings, "  ", "  ")
	os.WriteFile(settingsPath, b, 0644)
	if db != nil {
		dbPersistAll()
	}
}

func dbPersistAll() {
	if db == nil {
		return
	}
	kvSet("pin", settings.Pin)
	kvSet("oled_line1", settings.OLEDLine1)
	kvSet("oled_line2", settings.OLEDLine2)
	kvSet("oled_line3", settings.OLEDLine3)
	kvSet("oled_line4", settings.OLEDLine4)
	kvSet("poll_interval", fmt.Sprint(settings.PollInterval))
	kvSet("cron_api_key", settings.CronAPIKey)
	kvSet("cron_token", settings.CronToken)
	kvSet("public_url", settings.PublicURL)
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
		"relays":        settings.Relays,
		"sensors":       settings.Sensors,
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
		CronAPIKey   string `json:"cron_api_key"`
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
	if req.CronAPIKey != "" {
		settings.CronAPIKey = req.CronAPIKey
	}
	settings.mu.Unlock()
	saveSettings(settings)
	addLog(realIP(r), "settings: update")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// relay/sensor defs CRUD
func handleRelays(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		settings.mu.RLock()
		json.NewEncoder(w).Encode(settings.Relays)
		settings.mu.RUnlock()
	case "POST":
		var d RelayDef
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil || d.ID == "" {
			http.Error(w, `{"error":"id required"}`, 400)
			return
		}
		// id sanitize: lower alnum + _-
		if d.Label == "" {
			d.Label = d.ID
		}
		settings.mu.Lock()
		for _, e := range settings.Relays {
			if e.ID == d.ID {
				settings.mu.Unlock()
				http.Error(w, `{"error":"id sudah ada"}`, 400)
				return
			}
		}
		settings.Relays = append(settings.Relays, d)
		if db != nil { db.Exec(`INSERT OR IGNORE INTO relays(id,label,icon) VALUES(?,?,?)`, d.ID, d.Label, d.Icon) }
		// also init state relay false
		st.mu.Lock()
		if st.Relays == nil {
			st.Relays = map[string]bool{}
		}
		if _, ok := st.Relays[d.ID]; !ok {
			st.Relays[d.ID] = false
		}
		st.mu.Unlock()
		settings.mu.Unlock()
		saveSettings(settings)
		addLog(realIP(r), "relay +"+d.ID)
		json.NewEncoder(w).Encode(d)
	case "DELETE":
		id := r.URL.Query().Get("id")
		if id == "" {
			// also try path /api/relays/{id}
			id = r.PathValue("id")
		}
		if id == "" {
			http.Error(w, `{"error":"id required"}`, 400)
			return
		}
		settings.mu.Lock()
		idx := -1
		for i, e := range settings.Relays {
			if e.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			settings.mu.Unlock()
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		settings.Relays = append(settings.Relays[:idx], settings.Relays[idx+1:]...)
		if db != nil { db.Exec(`DELETE FROM relays WHERE id=?`, id) }
		settings.mu.Unlock()
		saveSettings(settings)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"GET/POST/DELETE"}`, 405)
	}
}

func handleSensors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		settings.mu.RLock()
		json.NewEncoder(w).Encode(settings.Sensors)
		settings.mu.RUnlock()
	case "POST":
		var d SensorDef
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil || d.ID == "" {
			http.Error(w, `{"error":"id required"}`, 400)
			return
		}
		if d.Label == "" {
			d.Label = d.ID
		}
		settings.mu.Lock()
		for _, e := range settings.Sensors {
			if e.ID == d.ID {
				settings.mu.Unlock()
				http.Error(w, `{"error":"id sudah ada"}`, 400)
				return
			}
		}
		settings.Sensors = append(settings.Sensors, d)
		if db != nil { db.Exec(`INSERT OR IGNORE INTO sensors(id,label,unit,icon) VALUES(?,?,?,?)`, d.ID, d.Label, d.Unit, d.Icon) }
		settings.mu.Unlock()
		// init sensor value 0
		st.mu.Lock()
		if st.Sensors == nil {
			st.Sensors = map[string]float64{}
		}
		if _, ok := st.Sensors[d.ID]; !ok {
			st.Sensors[d.ID] = 0
		}
		st.mu.Unlock()
		saveSettings(settings)
		addLog(realIP(r), "sensor +"+d.ID)
		json.NewEncoder(w).Encode(d)
	case "DELETE":
		id := r.URL.Query().Get("id")
		if id == "" {
			id = r.PathValue("id")
		}
		if id == "" {
			http.Error(w, `{"error":"id required"}`, 400)
			return
		}
		settings.mu.Lock()
		idx := -1
		for i, e := range settings.Sensors {
			if e.ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			settings.mu.Unlock()
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		settings.Sensors = append(settings.Sensors[:idx], settings.Sensors[idx+1:]...)
		if db != nil { db.Exec(`DELETE FROM sensors WHERE id=?`, id) }
		settings.mu.Unlock()
		saveSettings(settings)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"GET/POST/DELETE"}`, 405)
	}
}
