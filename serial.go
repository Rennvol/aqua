package main

import (
	"bufio"
	"encoding/json"
	"log"
	"math"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Serial link STB <-> UNO, 9600 baud, 1 baris JSON + \n.
// ponytail: baud diset via `stty` sekali saat open, bukan lib serial;
// upgrade ke lib (go.bug.st/serial) kalau butuh auto-reconnect canggih.
var (
	serialMu   sync.Mutex
	serialFile *os.File
)

var serialCandidates = []string{"/dev/ttyACM0", "/dev/ttyUSB0"}

// serialOpen buka port pertama yang ada. Return nil kalau tak ada (caller fallback mock).
func serialOpen() *os.File {
	for _, dev := range serialCandidates {
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		// set baud 9600 raw via stty (stdlib tak bisa set baud sendiri)
		_ = exec.Command("stty", "-F", dev, "9600", "raw", "-echo").Run()
		f, err := os.OpenFile(dev, os.O_RDWR, 0600)
		if err != nil {
			log.Printf("serial: buka %s gagal: %v", dev, err)
			continue
		}
		log.Printf("serial: tersambung %s @9600", dev)
		return f
	}
	return nil
}

func serialWriteLine(s string) {
	serialMu.Lock()
	defer serialMu.Unlock()
	if serialFile == nil {
		serialFile = serialOpen()
		if serialFile == nil {
			return // tak ada UNO, diam (mock yang jalan)
		}
	}
	serialFile.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := serialFile.WriteString(s + "\n"); err != nil {
		log.Printf("serial: tulis gagal: %v", err)
		serialFile.Close()
		serialFile = nil
	}
}

// serialPushOLED kirim 4 baris render saat ini ke OLED via UNO.
// Kalau OLED dimatikan: kirim 4 baris kosong = piksel mati (tanpa ubah firmware).
// Kalau screensaver jam tampil: diam, jangan timpa (cek ClockUntil).
func serialPushOLED() {
	st.mu.RLock()
	on := st.OLEDOn
	until := st.ClockUntil
	always := st.ClockAlwaysOn
	st.mu.RUnlock()
	if always && on {
		return // jam terus aktif, jangan timpa
	}
	if time.Now().Unix() < until {
		return // jam besar tampil, push dashboard ditahan
	}
	var lines [4]string
	if on {
		lines = oledLines()
	}
	b, _ := json.Marshal(map[string]interface{}{"cmd": "oled", "lines": lines[:]})
	serialWriteLine(string(b))
}

// serialPushClock tampilkan jam besar (HH:MM WIB) selama dur detik, lalu balik dashboard.
// Kirim epoch UTC biar UNO sync detik 00 presisi (soft RTC + DS3231 optional).
func serialPushClock(dur int) {
	if dur <= 0 {
		dur = 10
	}
	st.mu.RLock()
	on := st.OLEDOn
	co := st.ClockOn
	st.mu.RUnlock()
	if !on || !co {
		return
	}
	txt := time.Now().In(time.FixedZone("WIB", 7*3600)).Format("15:04")
	epoch := time.Now().Unix()
	st.mu.Lock()
	st.ClockUntil = time.Now().Unix() + int64(dur)
	st.mu.Unlock()
	b, _ := json.Marshal(map[string]interface{}{"cmd": "clock", "text": txt, "dur": dur, "epoch": epoch})
	serialWriteLine(string(b))
	// balik dashboard otomatis setelah durasi (+1 dtk jeda)
	go func() {
		time.Sleep(time.Duration(dur+1) * time.Second)
		serialPushOLED()
	}()
}

// serialPushClockAlways ON/OFF jam-terus self-tick di UNO (detik 00 presisi, tanpa push per menit).
func serialPushClockAlways(on bool) {
	epoch := time.Now().Unix()
	txt := time.Now().In(time.FixedZone("WIB", 7*3600)).Format("15:04")
	b, _ := json.Marshal(map[string]interface{}{"cmd": "clockAlways", "on": map[bool]int{true: 1, false: 0}[on], "epoch": epoch, "text": txt})
	serialWriteLine(string(b))
}

func serialSyncTime() {
	b, _ := json.Marshal(map[string]interface{}{"cmd": "sync", "epoch": time.Now().Unix()})
	serialWriteLine(string(b))
}

// serialClockAlwaysLoop: push awal jika always aktif, lalu sync periodik biar drift <1s.
// UNO tick sendiri tiap menit, STB cuma sync. ponytail: sync 30 menit, ganti ke 5 menit jika drift terasa.
func serialClockAlwaysLoop() {
	time.Sleep(2 * time.Second)
	st.mu.RLock()
	always0 := st.ClockAlwaysOn
	on0 := st.OLEDOn
	st.mu.RUnlock()
	if always0 && on0 {
		serialPushClockAlways(true)
	}
	for {
		time.Sleep(30 * time.Minute)
		st.mu.RLock()
		always := st.ClockAlwaysOn
		on := st.OLEDOn
		st.mu.RUnlock()
		if always && on {
			serialSyncTime()
		}
	}
}

// serialPushRelay kirim perintah relay ke UNO (demo: lamp -> LED pin13).
func serialPushRelay(id string, on bool) {
	v := 0
	if on {
		v = 1
	}
	b, _ := json.Marshal(map[string]interface{}{"cmd": "relay", "id": id, "on": v})
	serialWriteLine(string(b))
}

// serialReadLoop baca JSON sensor dari UNO (nanti: {"temp":..,"voltage":..}).
// Baris non-JSON / rusak diabaikan.
func serialReadLoop() {
	for {
		serialMu.Lock()
		f := serialFile
		serialMu.Unlock()
		if f == nil {
			time.Sleep(3 * time.Second)
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024), 1024)
		// scanner terikat file lama; kalau reconnect, buat ulang:
		for scanner.Scan() {
			line := scanner.Text()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if _, ok := m["cmd"]; ok {
				continue // gema perintah, abaikan
			}
			for k, v := range m {
				var fval float64
				if err := json.Unmarshal(v, &fval); err == nil {
					st.setSensorGeneric(k, fval)
				}
			}
		}
		// scanner berhenti = port putus; reset agar reconnect
		serialMu.Lock()
		if serialFile != nil {
			serialFile.Close()
			serialFile = nil
		}
		serialMu.Unlock()
		time.Sleep(2 * time.Second)
	}
}

// serialOLEDLoop dorong tampilan OLED tiap PollInterval detik.
func serialOLEDLoop() {
	for {
		settings.mu.RLock()
		iv := settings.PollInterval
		settings.mu.RUnlock()
		if iv <= 0 {
			iv = 5
		}
		time.Sleep(time.Duration(iv) * time.Second)
		serialMu.Lock()
		has := serialFile != nil
		serialMu.Unlock()
		if has {
			serialPushOLED()
		}
	}
}

// RunMock simulates sensor data when no UNO is connected.
// Temperatures drifts around 28°C, voltage ~12V, current varies.
func RunMock() {
	st.mu.Lock()
	st.Mode = "mock"
	st.Connected = false
	st.mu.Unlock()

	t := 28.0
	v := 12.0
	i := 0.0
	step := 0.0
	lampOn := false
	fanOn := false

	for {
		step += 0.3
		// simulate temperature: 28±0.5°C with slow drift
		t = 28 + 0.5*math.Sin(step*0.1)
		// simulate voltage: 12.0±0.1V
		v = 12.0 + 0.1*math.Sin(step*0.05)
		// simulate current: varies 0.3-1.5A depending on loads
		base := 0.0
		if lampOn {
			base += 0.8
		}
		if fanOn {
			base += 0.3
		}
		i = base + 0.2*math.Sin(step*0.2)

		serialMu.Lock()
		hasSerial := serialFile != nil
		serialMu.Unlock()
		if !hasSerial {
			st.setSensor(t, v, math.Abs(i))
		}

		// read relay state from the shared state
		st.mu.RLock()
		lampOn = st.RelayLamp
		fanOn = st.RelayFan
		st.mu.RUnlock()

		time.Sleep(2 * time.Second)
	}
}

// RunSerial: coba serial real, fallback mock kalau UNO belum colok.
func RunSerial() {
	st.mu.Lock()
	st.Mode = "serial"
	st.mu.Unlock()

	serialMu.Lock()
	serialFile = serialOpen()
	has := serialFile != nil
	serialMu.Unlock()

	if has {
		st.mu.Lock()
		st.Connected = true
		st.mu.Unlock()
		log.Println("serial: mode real, UNO tersambung")
	} else {
		log.Println("serial: /dev/ttyACM0 tak ada, mode mock")
	}
	go serialReadLoop()
	go serialOLEDLoop()
	go serialClockAlwaysLoop()
	go watchSerialPresence()
	RunMock() // tetap jalan sebagai fallback sensor; diam saat serial aktif
}

// watchSerialPresence pantau colok/cabut UNO tanpa restart.
func watchSerialPresence() {
	for {
		time.Sleep(3 * time.Second)
		serialMu.Lock()
		has := serialFile != nil
		serialMu.Unlock()
		if has {
			continue
		}
		f := serialOpen()
		if f != nil {
			serialMu.Lock()
			serialFile = f
			serialMu.Unlock()
			st.mu.Lock()
			st.Connected = true
			st.mu.Unlock()
			log.Println("serial: UNO terdeteksi, pindah mode real")
			serialPushOLED()
		}
	}
}
