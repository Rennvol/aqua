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
func serialPushOLED() {
	lines := oledLines()
	b, _ := json.Marshal(map[string]interface{}{"cmd": "oled", "lines": lines[:]})
	serialWriteLine(string(b))
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
