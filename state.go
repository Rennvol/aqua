package main

import (
	"sync"
	"time"
)

// State is the shared sensor/relay state.
type State struct {
	mu          sync.RWMutex
	Connected   bool               `json:"connected"`
	Temperature float64            `json:"temperature"` // legacy, mirror Sensors["temp"]
	Voltage     float64            `json:"voltage"`     // legacy
	Current     float64            `json:"current"`     // legacy
	RelayLamp   bool               `json:"relay_lamp"`  // legacy mirror Relays["lamp"]
	RelayFan    bool               `json:"relay_fan"`   // legacy
	OLEDText    string             `json:"oled_text"`
	UpdatedAt   int64              `json:"updated_at"`
	Mode        string             `json:"mode"`
	HostTemp    float64            `json:"host_temp"`
	HostMemPct  float64            `json:"host_mem_pct"`
	HostLoad    float64            `json:"host_load"`
	Relays      map[string]bool    `json:"relays"`  // generic
	Sensors     map[string]float64 `json:"sensors"` // generic: temp, voltage, current, power, custom
}

// st is the shared state instance.
var st = &State{
	Mode:    "mock",
	Relays:  map[string]bool{"lamp": false, "fan": false},
	Sensors: map[string]float64{"temp": 0, "voltage": 0, "current": 0, "power": 0},
}

func (s *State) snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// copy maps
	rel := map[string]bool{}
	for k, v := range s.Relays {
		rel[k] = v
	}
	sens := map[string]float64{}
	for k, v := range s.Sensors {
		sens[k] = v
	}
	return State{
		Connected:   s.Connected,
		Temperature: s.Temperature,
		Voltage:     s.Voltage,
		Current:     s.Current,
		RelayLamp:   s.RelayLamp,
		RelayFan:    s.RelayFan,
		OLEDText:    s.OLEDText,
		UpdatedAt:   s.UpdatedAt,
		Mode:        s.Mode,
		HostTemp:    s.HostTemp,
		HostMemPct:  s.HostMemPct,
		HostLoad:    s.HostLoad,
		Relays:      rel,
		Sensors:     sens,
	}
}

func (s *State) setSensor(t, v, i float64) {
	s.mu.Lock()
	s.Temperature = t
	s.Voltage = v
	s.Current = i
	if s.Sensors == nil {
		s.Sensors = map[string]float64{}
	}
	s.Sensors["temp"] = t
	s.Sensors["voltage"] = v
	s.Sensors["current"] = i
	s.Sensors["power"] = v * i
	s.UpdatedAt = time.Now().Unix()
	s.mu.Unlock()
	addHistory(t, v, i)
}

func (s *State) setSensorGeneric(id string, val float64) {
	s.mu.Lock()
	if s.Sensors == nil {
		s.Sensors = map[string]float64{}
	}
	s.Sensors[id] = val
	// mirror legacy
	switch id {
	case "temp":
		s.Temperature = val
	case "voltage":
		s.Voltage = val
	case "current":
		s.Current = val
	}
	s.UpdatedAt = time.Now().Unix()
	s.mu.Unlock()
}

func (s *State) setRelay(which string, on bool) {
	s.mu.Lock()
	if s.Relays == nil {
		s.Relays = map[string]bool{}
	}
	s.Relays[which] = on
	switch which {
	case "lamp":
		s.RelayLamp = on
	case "fan":
		s.RelayFan = on
	}
	s.UpdatedAt = time.Now().Unix()
	s.mu.Unlock()
}

func (s *State) getRelay(which string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Relays != nil {
		if v, ok := s.Relays[which]; ok {
			return v
		}
	}
	// fallback legacy
	if which == "lamp" {
		return s.RelayLamp
	}
	if which == "fan" {
		return s.RelayFan
	}
	return false
}
