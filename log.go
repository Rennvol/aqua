package main

import (
	"bufio"
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
	if db != nil {
		db.Exec(`INSERT INTO logs(time,ip,action) VALUES(?,?,?)`, e.Time, e.IP, e.Action)
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
	if db != nil {
		rows, err := db.Query(`SELECT time,ip,action FROM logs ORDER BY rowid DESC LIMIT 200`)
		if err == nil {
			defer rows.Close()
			var out []LogEntry
			for rows.Next() { var e LogEntry; rows.Scan(&e.Time, &e.IP, &e.Action); out = append(out, e) }
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { out[i], out[j] = out[j], out[i] }
			json.NewEncoder(w).Encode(out)
			return
		}
	}
	if logFile == nil {
		json.NewEncoder(w).Encode([]LogEntry{})
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	logFile.Sync()
	// ponytail: tail 200 via streaming, jangan ReadFile logs.jsonl besar (OOM sama kayak history)
	f, err := os.Open(logPath)
	if err != nil {
		json.NewEncoder(w).Encode([]LogEntry{})
		return
	}
	defer f.Close()
	// ring 200
	ring := make([]LogEntry, 200)
	n, total := 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var e LogEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		ring[n%200] = e
		n++
		total++
	}
	var out []LogEntry
	if total == 0 {
		json.NewEncoder(w).Encode(out)
		return
	}
	if total < 200 {
		out = make([]LogEntry, total)
		copy(out, ring[:total])
	} else {
		out = make([]LogEntry, 200)
		start := n % 200
		copy(out, ring[start:])
		copy(out[200-start:], ring[:start])
	}
	json.NewEncoder(w).Encode(out)
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
