package main

import (
	"bufio"
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
	historyMax  = 600
)

func init() {
	// ponytail: streaming tail 600, jangan ReadFile 169M (heap 700M+ -> pm2 400M -> loop restart 10s)
	f, err := os.Open(historyPath)
	if err != nil {
		return
	}
	defer f.Close()
	ring := make([]Point, historyMax)
	n := 0
	total := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var p Point
		if json.Unmarshal([]byte(line), &p) != nil {
			continue
		}
		ring[n%historyMax] = p
		n++
		total++
	}
	if total == 0 {
		return
	}
	if total < historyMax {
		historyData = make([]Point, total)
		copy(historyData, ring[:total])
	} else {
		historyData = make([]Point, historyMax)
		start := n % historyMax
		copy(historyData, ring[start:])
		copy(historyData[historyMax-start:], ring[:start])
	}
	if total > historyMax*2 {
		_ = f.Close()
		tmp := historyPath + ".tmp"
		out, err := os.Create(tmp)
		if err == nil {
			for _, p := range historyData {
				b, _ := json.Marshal(p)
				out.Write(append(b, '\n'))
			}
			out.Close()
			os.Rename(tmp, historyPath)
		}
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
	if db != nil {
		db.Exec(`INSERT OR IGNORE INTO history(t,temp,power,volt,curr) VALUES(?,?,?,?,?)`, p.T, p.Temp, p.Power, p.Volt, p.Curr)
		db.Exec(`DELETE FROM history WHERE t NOT IN (SELECT t FROM history ORDER BY t DESC LIMIT ?)`, historyMax)
	}
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
	out := make([]Point, len(historyData))
	copy(out, historyData)
	json.NewEncoder(w).Encode(out)
}
