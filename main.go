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
	"strconv"
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
	dbInit()
	syncSettingsFromDB()
	port := os.Getenv("PORT")
	if port == "" {
		port = "30221"
	}

	mux := http.NewServeMux()
	// public
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/api/cron/", handleCron)
	// protected (PIN)
	mux.HandleFunc("/api/state", authMW(handleState))
	mux.HandleFunc("/api/relay", authMW(handleRelay))
	mux.HandleFunc("/api/oled", authMW(handleOLED))
	mux.HandleFunc("/api/oled/power", authMW(handleOLEDPower))
	mux.HandleFunc("/api/clock/power", authMW(handleClockPower))
	mux.HandleFunc("/api/clock/always", authMW(handleClockAlwaysPower))
	mux.HandleFunc("/api/logs", authMW(handleLogs))
	mux.HandleFunc("/api/settings", authMW(handleSetSettings))
	mux.HandleFunc("/api/settings/get", authMW(handleGetSettings))
	mux.HandleFunc("/api/schedules", authMW(handleScheduleList))
	mux.HandleFunc("/api/schedule", authMW(handleScheduleAdd))
	mux.HandleFunc("/api/schedule/{id}/toggle", authMW(handleScheduleToggle))
	mux.HandleFunc("/api/schedule/{id}", authMW(handleScheduleDelete))
	mux.HandleFunc("/api/pin", authMW(handleChangePin))
	mux.HandleFunc("/api/history", authMW(handleHistory))
	mux.HandleFunc("/api/oled/preview", authMW(handleOLEDPreview))
	mux.HandleFunc("/api/relays", authMW(handleRelays))
	mux.HandleFunc("/api/relays/", authMW(handleRelays))
	mux.HandleFunc("/api/sensors", authMW(handleSensors))
	mux.HandleFunc("/api/sensors/", authMW(handleSensors))
	mux.HandleFunc("/api/ingest", authMW(handleIngest))

	go hostLoop()
	go RunSerial()
	ensureToken()

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

	fmt.Printf("🌊 santra — :%s\n", port)
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
	json.NewEncoder(w).Encode(st.snapshot())
}

type HealthResponse struct {
	Status string `json:"status"`
	Time   int64  `json:"time"`
	Uptime string `json:"uptime"`
}

/* ── relay ── */

func relayLabel(id string) string {
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	for _, d := range settings.Relays {
		if d.ID == id {
			return d.Label
		}
	}
	return id
}
func relayExists(id string) bool {
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	for _, d := range settings.Relays {
		if d.ID == id {
			return true
		}
	}
	return false
}

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
	if !relayExists(req.Relay) {
		json.NewEncoder(w).Encode(map[string]string{"error": "relay tidak dikenal"})
		return
	}

	st.setRelay(req.Relay, req.On)

	label := relayLabel(req.Relay)
	what := "ON"
	if !req.On {
		what = "OFF"
	}
	addLog(realIP(r), fmt.Sprintf("relay %s: %s", label, what))

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

	st.mu.Lock()
	st.OLEDText = req.Text
	st.mu.Unlock()

	addLog(realIP(r), fmt.Sprintf("oled: %s", req.Text))

	go serialPushOLED()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"text":   req.Text,
		"sent":   true,
	})
}

// handleOLEDPower ON/OFF layar. OFF = kirim 4 baris kosong (piksel mati,
// hemat + cegah burn-in). Tanpa ubah firmware: sketch lama tetap jalan.
func handleOLEDPower(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	st.mu.Lock()
	st.OLEDOn = req.On
	st.mu.Unlock()

	what := "OFF"
	if req.On {
		what = "ON"
	}
	addLog(realIP(r), "oled power: "+what)

	go serialPushOLED()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"on":     req.On,
	})
}

// handleClockPower ON/OFF screensaver jam besar tiap jam.
// ON = buat/aktifkan 1 job cron-job.org per jam menit 0 -> /api/cron/{token}/clock.
// OFF = job dinonaktifkan (tetap ada, hemat kuota create). Tanpa API key = mode lokal saja.
func handleClockPower(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	st.mu.Lock()
	if req.On && st.ClockAlwaysOn {
		st.ClockAlwaysOn = false
		kvSet("clock_always_on", "0")
	}
	st.ClockOn = req.On
	st.mu.Unlock()
	kvSet("clock_on", map[bool]string{true: "1", false: "0"}[req.On])
	saveSettings(settings)

	what := "OFF"
	if req.On {
		what = "ON"
	}
	addLog(realIP(r), "clock screensaver: "+what)

	if err := pushClockJob(req.On); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok-local", "on": req.On, "warn": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"on":     req.On,
		"job":    settings.ClockCronJobID,
	})
}

// handleClockAlwaysPower ON/OFF jam terus full layar. Saling kunci dengan ClockOn.
func handleClockAlwaysPower(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	st.mu.Lock()
	if req.On && st.ClockOn {
		st.ClockOn = false
		kvSet("clock_on", "0")
		_ = pushClockJob(false)
	}
	st.ClockAlwaysOn = req.On
	st.mu.Unlock()
	kvSet("clock_always_on", map[bool]string{true: "1", false: "0"}[req.On])
	saveSettings(settings)
	what := "OFF"
	if req.On {
		what = "ON"
	}
	addLog(realIP(r), "clock jam-terus: "+what)
	if req.On {
		// dorong langsung sekali, loop 60dtk yang lanjutkan
		go func() {
			st.mu.RLock()
			on := st.OLEDOn
			st.mu.RUnlock()
			if on {
				txt := timeNowWIB()
				b, _ := json.Marshal(map[string]interface{}{"cmd": "clock", "text": txt, "dur": 70})
				serialWriteLine(string(b))
			}
		}()
	} else {
		go serialPushOLED() // balik dashboard
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "on": req.On})
}

func timeNowWIB() string {
	return time.Now().In(time.FixedZone("WIB", 7*3600)).Format("15:04")
}

// clockURL hit-URL publik untuk screensaver jam (1x per jam, ringan).
func clockURL() string {
	settings.mu.RLock()
	tok := settings.CronToken
	base := settings.PublicURL
	settings.mu.RUnlock()
	if base == "" {
		base = "http://localhost:30221"
	}
	return strings.TrimRight(base, "/") + "/api/cron/" + tok + "/clock"
}

// pushClockJob buat/aktifkan 1 job per jam (minutes=[0], hours=all) di cron-job.org.
// ponytail: 1 job untuk 24 hit/hari; tambah jadwal kedua kalau butuh 2x/jam.
func pushClockJob(enabled bool) error {
	settings.mu.RLock()
	key := settings.CronAPIKey
	jobID := settings.ClockCronJobID
	settings.mu.RUnlock()
	if key == "" {
		return fmt.Errorf("cron_api_key belum diisi — jam jalan lokal saja, cron per jam belum dibuat")
	}
	payload := map[string]interface{}{
		"job": map[string]interface{}{
			"url":            clockURL(),
			"enabled":        enabled,
			"title":          "santra clock hourly",
			"saveResponses":  false,
			"requestTimeout": 30,
			"schedule": map[string]interface{}{
				"timezone": "Asia/Jakarta",
				"expiresAt": 0,
				"hours":    []int{-1},
				"mdays":    []int{-1},
				"minutes":  []int{0},
				"months":   []int{-1},
				"wdays":    []int{-1},
			},
		},
	}
	if jobID == 0 {
		data, code, err := cronReq("PUT", "/jobs", mustJSON(payload))
		if err != nil {
			return err
		}
		if code != 200 {
			return fmt.Errorf("cron-job create HTTP %d: %s", code, string(data))
		}
		var out struct {
			JobID int `json:"jobId"`
		}
		if err := json.Unmarshal(data, &out); err != nil || out.JobID == 0 {
			return fmt.Errorf("cron-job create: bad response %s", string(data))
		}
		settings.mu.Lock()
		settings.ClockCronJobID = out.JobID
		settings.mu.Unlock()
		kvSet("clock_cron_job_id", strconv.Itoa(out.JobID))
		saveSettings(settings)
		return nil
	}
	data, code, err := cronReq("PATCH", fmt.Sprintf("/jobs/%d", jobID), mustJSON(payload))
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("cron-job update HTTP %d: %s", code, string(data))
	}
	return nil
}