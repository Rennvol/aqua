package main

import (
	"encoding/json"
	"net/http"
)

// POST /api/ingest { "temp":28.5, "ph":7.1, "tds":320 } atau { "sensors":{"ph":7.1} }
func handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"POST required"}`, 405)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	// allow wrapped {sensors:{...}}
	if v, ok := raw["sensors"]; ok {
		var m map[string]float64
		if err := json.Unmarshal(v, &m); err == nil {
			for k, val := range m {
				st.setSensorGeneric(k, val)
			}
			delete(raw, "sensors")
		}
	}
	// remaining top-level numeric fields -> sensors
	for k, v := range raw {
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			// skip known non-sensor keys
			if k == "pin" || k == "relay" || k == "on" {
				continue
			}
			st.setSensorGeneric(k, f)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
