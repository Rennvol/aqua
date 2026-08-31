package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Schedule is one relay on/off rule executed by cron-job.org.
type Schedule struct {
	ID        int    `json:"id"`
	Relay     string `json:"relay"`     // "lamp" | "fan"
	State     string `json:"state"`     // "on" | "off"
	Hour      int    `json:"hour"`      // 0-23
	Minute    int    `json:"minute"`    // 0-59
	Enabled   bool   `json:"enabled"`
	CronJobID int    `json:"cron_job_id"` // jobId dari cron-job.org
}

var cronAPIBase = "https://api.cron-job.org"

func genToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ensureToken generates CronToken once if empty.
func ensureToken() {
	settings.mu.Lock()
	defer settings.mu.Unlock()
	if settings.CronToken == "" {
		settings.CronToken = genToken()
		saveSettingsLocked()
	}
}

// cronURL builds the public hit-URL for a relay+st.
func cronURL(relay, state string) string {
	tok := ""
	settings.mu.RLock()
	tok = settings.CronToken
	base := settings.PublicURL
	settings.mu.RUnlock()
	if base == "" {
		base = "http://" + "localhost:30221"
	}
	return fmt.Sprintf("%s/api/cron/%s/%s/%s", strings.TrimRight(base, "/"), tok, relay, state)
}

func cronReq(method, path string, body []byte) ([]byte, int, error) {
	settings.mu.RLock()
	key := settings.CronAPIKey
	settings.mu.RUnlock()
	if key == "" {
		return nil, 0, fmt.Errorf("cron_api_key belum diisi di Settings")
	}
	var r *http.Request
	var err error
	if body != nil {
		r, err = http.NewRequest(method, cronAPIBase+path, bytes.NewReader(body))
	} else {
		r, err = http.NewRequest(method, cronAPIBase+path, nil)
	}
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// jobPayload builds the cron-job.org DetailedJob for a schedule.
func jobPayload(s *Schedule) map[string]interface{} {
	enabled := s.Enabled
	return map[string]interface{}{
		"job": map[string]interface{}{
			"url":            cronURL(s.Relay, s.State),
			"enabled":        enabled,
			"title":          fmt.Sprintf("aqua %s %s", s.Relay, s.State),
			"saveResponses":  false,
			"requestTimeout": 30,
			"schedule": map[string]interface{}{
				"timezone": "Asia/Jakarta",
				"expiresAt": 0,
				"hours":     []int{s.Hour},
				"mdays":     []int{-1},
				"minutes":   []int{s.Minute},
				"months":    []int{-1},
				"wdays":     []int{-1},
			},
		},
	}
}

// pushSchedule creates (or updates) the cron-job.org job for s.
func pushSchedule(s *Schedule) error {
	if s.CronJobID == 0 {
		// create
		data, code, err := cronReq("PUT", "/jobs", mustJSON(jobPayload(s)))
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
		s.CronJobID = out.JobID
		return nil
	}
	// update (toggle enabled / change time)
	data, code, err := cronReq("PATCH", fmt.Sprintf("/jobs/%d", s.CronJobID), mustJSON(jobPayload(s)))
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("cron-job update HTTP %d: %s", code, string(data))
	}
	return nil
}

func deleteScheduleRemote(s *Schedule) error {
	if s.CronJobID == 0 {
		return nil
	}
	data, code, err := cronReq("DELETE", fmt.Sprintf("/jobs/%d", s.CronJobID), nil)
	if err != nil {
		return err
	}
	if code != 200 && code != 404 {
		return fmt.Errorf("cron-job delete HTTP %d: %s", code, string(data))
	}
	return nil
}

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

/* ── HTTP handlers ── */

func handleScheduleList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	settings.mu.RLock()
	s := make([]Schedule, len(settings.Schedules))
	copy(s, settings.Schedules)
	settings.mu.RUnlock()
	json.NewEncoder(w).Encode(s)
}

func handleScheduleAdd(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	var req Schedule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	if req.Relay != "lamp" && req.Relay != "fan" {
		json.NewEncoder(w).Encode(map[string]string{"error": "relay must be lamp/fan"})
		return
	}
	if req.State != "on" && req.State != "off" {
		json.NewEncoder(w).Encode(map[string]string{"error": "state must be on/off"})
		return
	}
	if req.Hour < 0 || req.Hour > 23 || req.Minute < 0 || req.Minute > 59 {
		json.NewEncoder(w).Encode(map[string]string{"error": "jam di luar range"})
		return
	}

	// assign id
	settings.mu.Lock()
	maxID := 0
	for _, s := range settings.Schedules {
		if s.ID > maxID {
			maxID = s.ID
		}
	}
	req.ID = maxID + 1
	req.Enabled = true
	// derive public base URL from this request (scheme + host)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if settings.PublicURL == "" && r.Host != "" {
		settings.PublicURL = scheme + "://" + r.Host
	}
	settings.Schedules = append(settings.Schedules, req)
	newSched := req // copy before unlock
	settings.mu.Unlock()
	saveSettings(settings)

	if err := pushSchedule(&newSched); err != nil {
		// rollback: jangan simpan jadwal yang gagal dibuat di cron-job.org
		settings.mu.Lock()
		for i := range settings.Schedules {
			if settings.Schedules[i].ID == newSched.ID {
				settings.Schedules = append(settings.Schedules[:i], settings.Schedules[i+1:]...)
				break
			}
		}
		settings.mu.Unlock()
		saveSettings(settings)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
		return
	}
	// persist cron_job_id
	settings.mu.Lock()
	for i := range settings.Schedules {
		if settings.Schedules[i].ID == newSched.ID {
			settings.Schedules[i].CronJobID = newSched.CronJobID
		}
	}
	settings.mu.Unlock()
	saveSettings(settings)

	addLog(realIP(r), fmt.Sprintf("jadwal +%s %s %02d:%02d (cron %d)", req.Relay, req.State, req.Hour, req.Minute, newSched.CronJobID))
	json.NewEncoder(w).Encode(newSched)
}

func handleScheduleToggle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	// /api/schedule/{id}/toggle
	id, _ := strconv.Atoi(r.PathValue("id"))

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}

	var sched *Schedule
	settings.mu.Lock()
	for i := range settings.Schedules {
		if settings.Schedules[i].ID == id {
			sched = &settings.Schedules[i]
		}
	}
	if sched == nil {
		settings.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"error": "jadwal tak ditemukan"})
		return
	}
	sched.Enabled = req.Enabled
	settings.mu.Unlock()
	saveSettings(settings)

	if err := pushSchedule(sched); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
		return
	}
	st := "aktif"
	if !req.Enabled {
		st = "nonaktif"
	}
	addLog(realIP(r), fmt.Sprintf("jadwal %d %s", id, st))
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "DELETE" {
		json.NewEncoder(w).Encode(map[string]string{"error": "DELETE required"})
		return
	}
	// /api/schedule/{id}
	id, _ := strconv.Atoi(r.PathValue("id"))

	var removed *Schedule
	settings.mu.Lock()
	for i := range settings.Schedules {
		if settings.Schedules[i].ID == id {
			cp := settings.Schedules[i]
			removed = &cp
			settings.Schedules = append(settings.Schedules[:i], settings.Schedules[i+1:]...)
			break
		}
	}
	settings.mu.Unlock()
	if removed == nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "jadwal tak ditemukan"})
		return
	}
	saveSettings(settings)

	if err := deleteScheduleRemote(removed); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
		return
	}
	addLog(realIP(r), fmt.Sprintf("jadwal -%d %s", removed.ID, removed.Relay))
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleCron executes a relay on/off from cron-job.org hit.
// GET /api/cron/{token}/{relay}/{state}
func handleCron(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "cron" {
		http.Error(w, "not found", 404)
		return
	}
	tok := parts[2]
	relay := parts[3]
	state := parts[4]

	settings.mu.RLock()
	valid := tok != "" && tok == settings.CronToken
	settings.mu.RUnlock()
	if !valid {
		http.Error(w, "bad token", 403)
		return
	}
	if relay != "lamp" && relay != "fan" {
		http.Error(w, "bad relay", 400)
		return
	}
	if state != "on" && state != "off" {
		http.Error(w, "bad state", 400)
		return
	}

	st.setRelay(relay, state == "on")
	label := "lampu"
	if relay == "fan" {
		label = "kipas"
	}
	addLog(realIP(r), fmt.Sprintf("auto cron %s: %s", label, strings.ToUpper(state)))
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "relay": relay, "on": state})
}