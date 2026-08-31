package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

type LogEntry struct {
	Time   string `json:"time"`
	IP     string `json:"ip"`
	Action string `json:"action"`
}

var (
	logMu    sync.Mutex
	logPath  = "logs.jsonl"
	logFile  *os.File
)

func initLog() {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logFile = nil
		return
	}
	logFile = f
}

func addLog(ip, action string) {
	e := LogEntry{Time: time.Now().Format("2006-01-02 15:04:05"), IP: ip, Action: action}
	b, _ := json.Marshal(e)
	if logFile != nil {
		logMu.Lock()
		logFile.Write(append(b, '\n'))
		logMu.Unlock()
	}
}

// accessLog middleware: log page loads (GET /) debounced per-IP (60s)
// ponytail: in-memory map last-hit, fine for single user; swap to file/DB if multi-user
var (
	accessMu    sync.Mutex
	lastAccess  = map[string]time.Time{}
)

func accessLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			ip := realIP(r)
			accessMu.Lock()
			last, ok := lastAccess[ip]
			now := time.Now()
			if !ok || now.Sub(last) > 60*time.Second {
				lastAccess[ip] = now
				accessMu.Unlock()
				addLog(ip, "akses web")
			} else {
				accessMu.Unlock()
			}
		}
		next.ServeHTTP(w, r)
	})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if logFile == nil {
		json.NewEncoder(w).Encode([]LogEntry{})
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	logFile.Sync()
	data, err := os.ReadFile(logPath)
	if err != nil {
		json.NewEncoder(w).Encode([]LogEntry{})
		return
	}
	// parse all, keep last 200
	var all []LogEntry
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var e LogEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			all = append(all, e)
		}
	}
	if len(all) > 200 {
		all = all[len(all)-200:]
	}
	json.NewEncoder(w).Encode(all)
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
