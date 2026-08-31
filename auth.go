package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// in-memory session tokens (no DB needed for single user)
var sessions = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func newSession() string {
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	sessions.Lock()
	sessions.m[tok] = time.Now().Add(24 * time.Hour)
	sessions.Unlock()
	return tok
}

func authOK(r *http.Request) bool {
	c, err := r.Cookie("aqua_session")
	if err != nil {
		return false
	}
	sessions.Lock()
	defer sessions.Unlock()
	exp, ok := sessions.m[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(sessions.m, c.Value)
		return false
	}
	return true
}

func authMW(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"auth"}`))
			return
		}
		next(w, r)
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"POST required"}`, 400)
		return
	}
	var req struct {
		Pin string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	settings.mu.RLock()
	correct := settings.Pin
	settings.mu.RUnlock()
	if correct == "" || req.Pin != correct {
		http.Error(w, `{"error":"pin salah"}`, 401)
		return
	}
	tok := newSession()
	http.SetCookie(w, &http.Cookie{
		Name:     "aqua_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleChangePin updates the PIN (from Settings UI).
func handleChangePin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"POST required"}`, 400)
		return
	}
	var req struct {
		Pin string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	if len(req.Pin) != 4 {
		http.Error(w, `{"error":"pin harus 4 angka"}`, 400)
		return
	}
	for _, c := range req.Pin {
		if c < '0' || c > '9' {
			http.Error(w, `{"error":"pin harus 4 angka"}`, 400)
			return
		}
	}
	settings.mu.Lock()
	settings.Pin = req.Pin
	settings.mu.Unlock()
	saveSettings(settings)
	addLog(realIP(r), "settings: ganti PIN")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if c, err := r.Cookie("aqua_session"); err == nil {
		sessions.Lock()
		delete(sessions.m, c.Value)
		sessions.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "aqua_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
