package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

type Point struct {
	T     int64   `json:"t"`
	Temp  float64 `json:"temp"`
	Power float64 `json:"power"`
	Volt  float64 `json:"volt"`
	Curr  float64 `json:"curr"`
}

var (
	historyMu   sync.Mutex
	historyData []Point
	historyPath = "history.jsonl"
	historyMax  = 600 // ~20 menit @ 2s/sample, cukup buat sparkline; ponytail: ring file jika >10k
)

func init() {
	// load tail if exists
	b, err := os.ReadFile(historyPath)
	if err != nil {
		return
	}
	for _, line := range splitLines(string(b)) {
		if line == "" {
			continue
		}
		var p Point
		if json.Unmarshal([]byte(line), &p) == nil {
			historyData = append(historyData, p)
		}
	}
	if len(historyData) > historyMax {
		historyData = historyData[len(historyData)-historyMax:]
	}
}

func addHistory(temp, volt, curr float64) {
	p := Point{T: time.Now().Unix(), Temp: temp, Power: volt * curr, Volt: volt, Curr: curr}
	historyMu.Lock()
	historyData = append(historyData, p)
	if len(historyData) > historyMax {
		historyData = historyData[len(historyData)-historyMax:]
	}
	historyMu.Unlock()
	// also insert DB
	if db != nil {
		db.Exec(`INSERT OR IGNORE INTO history(t,temp,power,volt,curr) VALUES(?,?,?,?,?)`, p.T, p.Temp, p.Power, p.Volt, p.Curr)
		// prune keep 600
		db.Exec(`DELETE FROM history WHERE t NOT IN (SELECT t FROM history ORDER BY t DESC LIMIT ?)`, historyMax)
	}
	// append file best-effort
	b, _ := json.Marshal(p)
	f, err := os.OpenFile(historyPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.Write(append(b, '\n'))
		f.Close()
	}
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	historyMu.Lock()
	defer historyMu.Unlock()
	// return copy
	out := make([]Point, len(historyData))
	copy(out, historyData)
	json.NewEncoder(w).Encode(out)
}
