package main

import (
	"sync"
	"time"
)

// State is the shared sensor/relay state.
type State struct {
	mu          sync.RWMutex
	Connected   bool    `json:"connected"`
	Temperature float64 `json:"temperature"`
	Voltage     float64 `json:"voltage"`
	Current     float64 `json:"current"`
	RelayLamp   bool    `json:"relay_lamp"`
	RelayFan    bool    `json:"relay_fan"`
	OLEDText    string  `json:"oled_text"`
	UpdatedAt   int64   `json:"updated_at"`
	Mode        string  `json:"mode"` // "mock" | "serial"
}

// st is the shared state instance.
var st = &State{Mode: "mock"}

func (s *State) snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	}
}

func (s *State) setSensor(t, v, i float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Temperature = t
	s.Voltage = v
	s.Current = i
	s.UpdatedAt = time.Now().Unix()
}

func (s *State) setRelay(which string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch which {
	case "lamp":
		s.RelayLamp = on
	case "fan":
		s.RelayFan = on
	}
	s.UpdatedAt = time.Now().Unix()
}
