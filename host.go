package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// host stats — dibaca dari /proc (STB Armbian / Oracle sama), tanpa dep tambahan.
// ponytail: polling 5s, global fields di st; upgrade ke per-field cache jika butuh.
var prevCpuTotal, prevCpuIdle uint64
var prevNetRx, prevNetTx uint64
var prevNetTime time.Time
var prevNetIF string
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
	ipLocal, ipTail := getNetIPs()
	st.mu.Lock()
	st.HostIPLocal = ipLocal
	st.HostIPTail = ipTail
	st.mu.Unlock()
	var sfs syscall.Statfs_t
	if err := syscall.Statfs("/", &sfs); err == nil {
		total := sfs.Blocks * uint64(sfs.Bsize)
		free := sfs.Bavail * uint64(sfs.Bsize)
		used := total - free
		pct := 0.0
		if total > 0 {
			pct = float64(used) / float64(total) * 100
		}
		st.mu.Lock()
		st.HostDiskPct = pct
		st.HostDiskFree = fmtBytes(free)
		st.mu.Unlock()
	}
	// cpu % from /proc/stat (delta 5s)
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		var total, idle uint64
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "cpu ") {
				f := strings.Fields(l)
				for i := 1; i < len(f); i++ {
					v, _ := strconv.ParseUint(f[i], 10, 64)
					total += v
					if i == 4 {
						idle = v
					}
				}
				break
			}
		}
		if prevCpuTotal != 0 && total > prevCpuTotal {
			dTotal := float64(total - prevCpuTotal)
			dIdle := float64(idle - prevCpuIdle)
			pct := (1 - dIdle/dTotal) * 100
			if pct < 0 { pct = 0 }
			if pct > 100 { pct = 100 }
			st.mu.Lock(); st.HostCpuPct = pct; st.mu.Unlock()
		}
		prevCpuTotal, prevCpuIdle = total, idle
	}
	// net rx/tx rate from /proc/net/dev (pick busiest non-lo iface)
	if b, err := os.ReadFile("/proc/net/dev"); err == nil {
		bestIF := ""; var bestRx, bestTx uint64; bestSum := uint64(0)
		for _, l := range strings.Split(string(b), "\n") {
			if !strings.Contains(l, ":") { continue }
			parts := strings.Split(l, ":")
			if len(parts) != 2 { continue }
			iface := strings.TrimSpace(parts[0])
			if iface == "lo" { continue }
			f := strings.Fields(parts[1])
			if len(f) < 10 { continue }
			rx, _ := strconv.ParseUint(f[0], 10, 64)
			tx, _ := strconv.ParseUint(f[8], 10, 64)
			if rx+tx > bestSum { bestSum = rx+tx; bestIF = iface; bestRx = rx; bestTx = tx }
		}
		if bestIF != "" {
			now := time.Now()
			if !prevNetTime.IsZero() && prevNetIF == bestIF {
				dt := now.Sub(prevNetTime).Seconds()
				if dt > 0 {
					rxRate := float64(bestRx-prevNetRx) / dt
					txRate := float64(bestTx-prevNetTx) / dt
					// clamp negative (counter reset)
					if bestRx < prevNetRx { rxRate = 0 }
					if bestTx < prevNetTx { txRate = 0 }
					st.mu.Lock()
					st.HostNetIF = bestIF
					st.HostNetRx = fmtBytes(uint64(rxRate)) + "/s"
					st.HostNetTx = fmtBytes(uint64(txRate)) + "/s"
					st.mu.Unlock()
				}
			} else {
				st.mu.Lock(); st.HostNetIF = bestIF; st.mu.Unlock()
			}
			prevNetRx, prevNetTx, prevNetTime, prevNetIF = bestRx, bestTx, now, bestIF
		}
	}
	// host model (STB Armbian) — cache once
	if st.HostModel == "" {
		model := ""
		if b, err := os.ReadFile("/proc/device-tree/model"); err == nil {
			model = strings.Trim(strings.TrimSpace(string(b)), "\x00")
		}
		if model == "" {
			if b, err := os.ReadFile("/etc/armbian-release"); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(l, "BOARD=") { model = strings.Trim(strings.Trim(l[6:], "\" \""), " "); break }
				}
			}
		}
		if model == "" {
			if b, err := os.ReadFile("/etc/os-release"); err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(l, "PRETTY_NAME=") { model = strings.Trim(strings.Trim(l[13:], "\"\""), " "); break }
				}
			}
		}
		if len(model) > 28 { model = model[:28] }
		if model != "" { st.mu.Lock(); st.HostModel = model; st.mu.Unlock() }
	}
	ora := getOracleTail(ipTail)
	st.mu.Lock()
	st.HostOracleTail = ora
	st.mu.Unlock()
}

func getNetIPs() (local, tail string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			s := ip.String()
			if strings.HasPrefix(s, "100.") {
				if tail == "" {
					tail = s
				}
			} else if strings.HasPrefix(s, "192.168.") || strings.HasPrefix(s, "10.") || strings.HasPrefix(s, "172.") {
				if local == "" {
					local = s
				}
			} else if local == "" && !strings.HasPrefix(s, "127.") {
				local = s
			}
		}
	}
	if tail == "" {
		if iface, err := net.InterfaceByName("tailscale0"); err == nil {
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok {
					if ip := ipnet.IP.To4(); ip != nil {
						tail = ip.String()
						break
					}
				}
			}
		}
	}
	return local, tail
}

func getOracleTail(selfTail string) string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	var data struct {
		Self struct {
			HostName     string   `json:"hostName"`
			DNSName      string   `json:"dnsName"`
			TailscaleIPs []string `json:"tailscaleIPs"`
		} `json:"Self"`
		Peer map[string]struct {
			HostName     string   `json:"hostName"`
			DNSName      string   `json:"dnsName"`
			TailscaleIPs []string `json:"tailscaleIPs"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return ""
	}
	// di STB cari peer oracle
	for _, pp := range data.Peer {
		name := strings.ToLower(pp.HostName + " " + pp.DNSName)
		if strings.Contains(name, "oracle") {
			if len(pp.TailscaleIPs) > 0 && pp.TailscaleIPs[0] != selfTail {
				return pp.TailscaleIPs[0]
			}
		}
	}
	// di Oracle sendiri: Self = oracle -> pakai Self IP
	if len(data.Self.TailscaleIPs) > 0 {
		name := strings.ToLower(data.Self.HostName + " " + data.Self.DNSName)
		if strings.Contains(name, "oracle") {
			return data.Self.TailscaleIPs[0]
		}
	}
	return ""
}

func fmtBytes(b uint64) string {
	if b >= 1<<30 {
		return strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64) + "GB"
	}
	if b >= 1<<20 {
		return strconv.FormatFloat(float64(b)/(1<<20), 'f', 0, 64) + "MB"
	}
	if b >= 1<<10 {
		return strconv.FormatFloat(float64(b)/(1<<10), 'f', 0, 64) + "KB"
	}
	return fmt.Sprint(b) + "B"
}

func parseMemKB(line string) int64 {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(f[1], 10, 64)
	return v
}

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
	hostIPLocal := st.HostIPLocal
	hostIPTail := st.HostIPTail
	hostOracleTail := st.HostOracleTail
	hostDiskPct := st.HostDiskPct
	hostDiskFree := st.HostDiskFree
	hostCpuPct := st.HostCpuPct
	hostNetRx := st.HostNetRx
	hostNetTx := st.HostNetTx
	energyTodayKwh := st.EnergyTodayKwh
	energyMonthKwh := st.EnergyMonthKwh
	energyCostToday := st.EnergyCostToday
	energyCostMonth := st.EnergyCostMonth
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
	case "stb_ip_local":
		if hostIPLocal == "" {
			return "- IP lokal"
		}
		return hostIPLocal
	case "stb_ip_tail":
		if hostIPTail == "" {
			return "- tail STB"
		}
		return hostIPTail
	case "oracle_tail":
		if hostOracleTail == "" {
			return "- tail VMO"
		}
		return hostOracleTail
	case "stb_cpu":
		return strconv.FormatFloat(hostCpuPct, 'f', 0, 64) + "% CPU"
	case "stb_net":
		if hostNetRx == "" && hostNetTx == "" {
			return "- net"
		}
		// ponytail: 21 char limit — iface omitted, just rates
		return "\u2193" + hostNetRx + " \u2191" + hostNetTx
	case "stb_model":
		{ m := st.HostModel; if m == "" { return "- model" }; if len([]rune(m)) > 21 { m = string([]rune(m)[:21]) }; return m }
	case "stb_disk":
		if hostDiskFree == "" {
			return strconv.FormatFloat(hostDiskPct, 'f', 0, 64) + "% disk"
		}
		return strconv.FormatFloat(hostDiskPct, 'f', 0, 64) + "% " + hostDiskFree + " free"
	case "energy_today":
		if energyTodayKwh == 0 && energyCostToday == 0 {
			return "- energi hr"
		}
		s := fmt.Sprintf("%.2fkWh Rp%d", energyTodayKwh, energyCostToday)
		if len([]rune(s)) > 21 {
			s = fmt.Sprintf("%.2fkWh", energyTodayKwh)
		}
		return s
	case "energy_month":
		if energyMonthKwh == 0 && energyCostMonth == 0 {
			return "- energi bln"
		}
		s := fmt.Sprintf("%.2fkWh Rp%d", energyMonthKwh, energyCostMonth)
		if len([]rune(s)) > 21 {
			s = fmt.Sprintf("%.2fkWh", energyMonthKwh)
		}
		return s
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
	r1, r2, r3, r4 := settings.OLEDLine1R, settings.OLEDLine2R, settings.OLEDLine3R, settings.OLEDLine4R
	custom := settings.OLEDTextCustom
	settings.mu.RUnlock()
	if custom != "" {
		parts := splitLines(custom)
		var out [4]string
		for i:=0;i<4 && i<len(parts);i++ { out[i]=fitOLED(parts[i]) }
		return out
	}
	return [4]string{renderPair(l1, r1), renderPair(l2, r2), renderPair(l3, r3), renderPair(l4, r4)}
}

func renderPair(lKey, rKey string) string {
	if rKey == "" || rKey == "-" {
		return fitOLED(renderOLEDLine(lKey))
	}
	l := renderOLEDLine(lKey)
	r := renderOLEDLine(rKey)
	if lKey == "relay" {
		l = relayCompact()
	}
	if rKey == "relay" {
		r = relayCompact()
	}
	lr, rr := []rune(l), []rune(r)
	if len(rr) > 10 {
		rr = rr[:10]
	}
	if len(lr)+1+len(rr) <= 21 {
		return string(lr) + " " + string(rr)
	}
	keep := 21 - 1 - len(rr)
	if keep < 0 {
		keep = 0
	}
	return string(lr[:keep]) + " " + string(rr)
}

func relayCompact() string {
	settings.mu.RLock()
	rels := append([]RelayDef(nil), settings.Relays...)
	settings.mu.RUnlock()
	st.mu.RLock()
	defer st.mu.RUnlock()
	parts := []string{}
	for _, rd := range rels {
		v := st.Relays[rd.ID]
		if rd.ID == "lamp" {
			v = st.RelayLamp
		}
		if rd.ID == "fan" {
			v = st.RelayFan
		}
		on := "OFF"
		if v {
			on = "ON"
		}
		ab := strings.ToUpper(string([]rune(rd.ID)[:1]))
		parts = append(parts, ab+":"+on)
	}
	if len(parts) == 0 {
		return "--"
	}
	return strings.Join(parts, " ")
}

func fitOLED(s string) string {
	r := []rune(s)
	if len(r) > 21 {
		return string(r[:21])
	}
	return s
}
