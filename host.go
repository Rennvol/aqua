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
	// RAM % dari /proc/meminfo
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
	// load 1m dari /proc/loadavg
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
	// suhu STB dari thermal_zone0 (jika ada, mis Armbian); Oracle x86 tidak ada -> 0
	if b, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			// nilai biasa milli-C
			c := float64(v) / 1000
			if c > 1000 { // beberapa device sudah dalam C
				c = float64(v)
			}
			st.mu.Lock()
			st.HostTemp = c
			st.mu.Unlock()
		}
	}
	// uptime tidak disimpan; snapshot pakai time.Since(startTime) di handler jika perlu
}

func parseMemKB(line string) int64 {
	// "MemTotal:       12213612 kB"
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(f[1], 10, 64)
	return v
}

// render satu line OLED berdasarkan key settings
func renderOLEDLine(key string) string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	switch key {
	case "temp":
		return strconv.FormatFloat(st.Temperature, 'f', 1, 64) + "C air"
	case "voltage":
		return strconv.FormatFloat(st.Voltage, 'f', 2, 64) + "V"
	case "current":
		return strconv.FormatFloat(st.Current, 'f', 2, 64) + "A"
	case "power":
		return strconv.FormatFloat(st.Voltage*st.Current, 'f', 1, 64) + "W"
	case "relay":
		l, f := "OFF", "OFF"
		if st.RelayLamp {
			l = "ON"
		}
		if st.RelayFan {
			f = "ON"
		}
		return "L:" + l + " F:" + f
	case "stb_temp":
		if st.HostTemp == 0 {
			return "STB --C"
		}
		return strconv.FormatFloat(st.HostTemp, 'f', 1, 64) + "C STB"
	case "stb_ram":
		return strconv.FormatFloat(st.HostMemPct, 'f', 0, 64) + "% RAM"
	case "stb_load":
		return strconv.FormatFloat(st.HostLoad, 'f', 2, 64) + " load"
	case "text":
		if st.OLEDText != "" {
			return st.OLEDText
		}
		return "-"
	default:
		return "-"
	}
}

func oledLines() [4]string {
	settings.mu.RLock()
	l1, l2, l3, l4 := settings.OLEDLine1, settings.OLEDLine2, settings.OLEDLine3, settings.OLEDLine4
	settings.mu.RUnlock()
	return [4]string{renderOLEDLine(l1), renderOLEDLine(l2), renderOLEDLine(l3), renderOLEDLine(l4)}
}
