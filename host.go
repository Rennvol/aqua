package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// host stats — dibaca dari /proc (STB Armbian / Oracle sama), tanpa dep tambahan.
// ponytail: polling 5s, global fields di st; upgrade ke per-field cache jika butuh.
func hostLoop() {
	updateHostStats()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		<-tick.C
		updateHostStats()
	}
}

func handleOLEDPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	lines := oledLines()
	json.NewEncoder(w).Encode(map[string]interface{}{"lines": lines})
}

func updateHostStats() {
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail int64
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "MemTotal:") {
				total = parseMemKB(l)
			} else if strings.HasPrefix(l, "MemAvailable:") {
				avail = parseMemKB(l)
			}
		}
		if total > 0 {
			pct := float64(total-avail) / float64(total) * 100
			st.mu.Lock()
			st.HostMemPct = pct
			st.mu.Unlock()
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				st.mu.Lock()
				st.HostLoad = v
				st.mu.Unlock()
			}
		}
	}
	if b, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			c := float64(v) / 1000
			if c > 1000 {
				c = float64(v)
			}
			st.mu.Lock()
			st.HostTemp = c
			st.mu.Unlock()
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				st.mu.Lock()
				st.HostUptime = v
				st.mu.Unlock()
			}
		}
	}
}

func parseMemKB(line string) int64 {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(f[1], 10, 64)
	return v
}

// fmtUptime format ringkas muat 20 char OLED: "3h12m", "2d5h", "45m", "30s".
// ponytail: tanpa tahun/bulan, uptime STB jarang lewat 30 hari.
func fmtUptime(sec float64) string {
	s := int64(sec)
	if s < 60 {
		return strconv.FormatInt(s, 10) + "s up"
	}
	m := s / 60
	if m < 60 {
		return strconv.FormatInt(m, 10) + "m up"
	}
	h := m / 60
	if h < 48 {
		return strconv.FormatInt(h, 10) + "h" + strconv.FormatInt(m%60, 10) + "m"
	}
	d := h / 24
	return strconv.FormatInt(d, 10) + "d" + strconv.FormatInt(h%24, 10) + "h"
}

func renderOLEDLine(key string) string {
	// snapshot under lock, then render without holding lock (avoid nested RLock deadlock)
	st.mu.RLock()
	temp := st.Temperature
	volt := st.Voltage
	curr := st.Current
	relayLamp := st.RelayLamp
	relayFan := st.RelayFan
	relaysCopy := map[string]bool{}
	for k, v := range st.Relays {
		relaysCopy[k] = v
	}
	sensorsCopy := map[string]float64{}
	for k, v := range st.Sensors {
		sensorsCopy[k] = v
	}
	hostTemp := st.HostTemp
	hostMem := st.HostMemPct
	hostLoad := st.HostLoad
	hostUptime := st.HostUptime
	oledText := st.OLEDText
	st.mu.RUnlock()

	switch key {
	case "temp":
		return strconv.FormatFloat(temp, 'f', 1, 64) + "C air"
	case "voltage":
		return strconv.FormatFloat(volt, 'f', 2, 64) + "V"
	case "current":
		return strconv.FormatFloat(curr, 'f', 2, 64) + "A"
	case "power":
		if v, ok := sensorsCopy["power"]; ok {
			return strconv.FormatFloat(v, 'f', 1, 64) + "W"
		}
		return strconv.FormatFloat(volt*curr, 'f', 1, 64) + "W"
	case "relay":
		settings.mu.RLock()
		rels := append([]RelayDef(nil), settings.Relays...)
		settings.mu.RUnlock()
		parts := []string{}
		for _, rd := range rels {
			v := relaysCopy[rd.ID]
			if rd.ID == "lamp" {
				v = relayLamp
			}
			if rd.ID == "fan" {
				v = relayFan
			}
			on := "OFF"
			if v {
				on = "ON"
			}
			parts = append(parts, rd.ID+":"+on)
		}
		if len(parts) == 0 {
			return "relay --"
		}
		s := strings.Join(parts, " ")
		if len(s) > 20 {
			s = s[:20]
		}
		return s
	case "stb_temp":
		if hostTemp == 0 {
			return "STB --C"
		}
		return strconv.FormatFloat(hostTemp, 'f', 1, 64) + "C STB"
	case "stb_ram":
		return strconv.FormatFloat(hostMem, 'f', 0, 64) + "% RAM"
	case "stb_load":
		return strconv.FormatFloat(hostLoad, 'f', 2, 64) + " load"
	case "stb_uptime":
		return fmtUptime(hostUptime)
	case "text":
		if oledText != "" {
			return oledText
		}
		return "-"
	default:
		if v, ok := sensorsCopy[key]; ok {
			unit := ""
			settings.mu.RLock()
			for _, sd := range settings.Sensors {
				if sd.ID == key {
					unit = sd.Unit
					break
				}
			}
			settings.mu.RUnlock()
			return strconv.FormatFloat(v, 'f', 1, 64) + unit + " " + key
		}
		return "-"
	}
}

func oledLines() [4]string {
	settings.mu.RLock()
	l1, l2, l3, l4 := settings.OLEDLine1, settings.OLEDLine2, settings.OLEDLine3, settings.OLEDLine4
	settings.mu.RUnlock()
	return [4]string{fitOLED(renderOLEDLine(l1)), fitOLED(renderOLEDLine(l2)), fitOLED(renderOLEDLine(l3)), fitOLED(renderOLEDLine(l4))}
}

// fitOLED potong per baris max 21 runes (font ncenB08 ~6px/char di 128px).
// Tanpa ini teks panjang overflow keluar layar (drawStr tak wrap).
func fitOLED(s string) string {
	r := []rune(s)
	if len(r) > 21 {
		return string(r[:21])
	}
	return s
}
