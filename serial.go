package main

import (
	"bufio"
	"encoding/json"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Serial link STB <-> UNO, 9600 baud, 1 baris JSON + \n.
// ponytail: baud diset via `stty` sekali saat open, bukan lib serial;
// upgrade ke lib (go.bug.st/serial) kalau butuh auto-reconnect canggih.
var (
	serialMu   sync.Mutex
	serialFile *os.File
	// serialFailStreak hitung gagal beruntun (tulis gagal / tanpa ack);
	// watchdog power-cycle UNO saat >= 2.
	serialFailStreak int
	// serialAckCh terima ack UNO {"ack":"lamp","on":1}; push relay tunggu ini.
	serialAckCh = make(chan serialAck, 8)
	// serialAckMu gilirkan tulis+tunggu-ack biar ack tak tertukar antar relay.
	serialAckMu sync.Mutex
)

// serialAck pengakuan UNO bahwa relay fisik sudah digerakkan.
type serialAck struct {
	id string
	on bool
}

var serialCandidates = []string{"/dev/ttyACM0", "/dev/ttyUSB0"}

// serialOpen buka port pertama yang ada. Return nil kalau tak ada (caller fallback mock).
func serialOpen() *os.File {
	for _, dev := range serialCandidates {
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		// set baud 9600 raw via stty (stdlib tak bisa set baud sendiri)
		_ = exec.Command("stty", "-F", dev, "9600", "raw", "-echo", "-hupcl").Run()
		// O_NOCTTY: UNO jangan jadi controlling terminal. Tanpa ini, proses
		// session-leader mengakuisisi ttyACM0 sebagai ctty; tiap power-cycle/
		// cabut → kernel kirim SIGHUP → proses mati di tengah jalan (sebelum
		// tulis authorized=1) → USB tertinggal mati + jadwal miss.
		fd, err := syscall.Open(dev, os.O_RDWR|syscall.O_NOCTTY, 0600)
		if err != nil {
			log.Printf("serial: buka %s gagal: %v", dev, err)
			continue
		}
		f := os.NewFile(uintptr(fd), dev)
		log.Printf("serial: tersambung %s @9600", dev)
		return f
	}
	return nil
}

func serialWriteLine(s string) bool {
	serialMu.Lock()
	defer serialMu.Unlock()
	if serialFile == nil {
		serialFile = serialOpen()
		if serialFile == nil {
			return false // tak ada UNO, diam (mock yang jalan)
		}
	}
	serialFile.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := serialFile.WriteString(s + "\n"); err != nil {
		log.Printf("serial: tulis gagal: %v — coba reconnect", err)
		serialFile.Close()
		serialFile = nil
		serialFile = serialOpen()
		if serialFile == nil {
			serialFailStreak++
			go serialWatchdog()
			return false
		}
		serialFile.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err2 := serialFile.WriteString(s + "\n"); err2 != nil {
			log.Printf("serial: retry gagal: %v", err2)
			serialFile.Close()
			serialFile = nil
			serialFailStreak++
			go serialWatchdog()
			return false
		}
	}
	serialFailStreak = 0
	return true
}

// serialWatchdog: 2x gagal beruntun = UNO hang → hard reset sekali
// (DTR, gagal lanjut power-cycle USB), lalu restore relay. Async.
func serialWatchdog() {
	serialMu.Lock()
	n := serialFailStreak
	serialMu.Unlock()
	if n < 2 {
		return
	}
	log.Printf("serial: watchdog %d gagal beruntun → hard reset UNO", n)
	if _, ok := serialResetUNOHard(); ok {
		serialMu.Lock()
		serialFailStreak = 0
		serialMu.Unlock()
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

// serialPushRelay kirim perintah relay ke UNO dan tunggu ack fisik.
// Return false = relay BELUM tentu gerak (web harus tampilkan gagal).
func serialPushRelay(id string, on bool) bool {
	serialAckMu.Lock()
	defer serialAckMu.Unlock()
	v := 0
	if on {
		v = 1
	}
	// kuras ack basi biar tak salah baca ack perintah lama
drain:
	for {
		select {
		case <-serialAckCh:
		default:
			break drain
		}
	}
	b, _ := json.Marshal(map[string]interface{}{"cmd": "relay", "id": id, "on": v})
	if !serialWriteLine(string(b)) {
		log.Printf("serial: relay %s -> %d GAGAL tulis (UNO tak terjangkau)", id, v)
		return false
	}
	// tunggu ack UNO max 2.5s; tanpa ack = relay diam walau tulis "sukses"
	timeout := time.After(2500 * time.Millisecond)
	for {
		select {
		case a := <-serialAckCh:
			if a.id == id {
				if a.on == on {
					serialFailStreak = 0
					log.Printf("serial: relay %s -> %d ACK ✓", id, v)
					return true
				}
				log.Printf("serial: relay %s ack beda state (minta %d, ack %v)", id, v, a.on)
				return false
			}
			// ack relay lain, simpan balik biar tak hilang
			select {
			case serialAckCh <- a:
			default:
			}
		case <-timeout:
			log.Printf("serial: relay %s -> %d TAK ADA ACK (relay diam!)", id, v)
			serialMu.Lock()
			serialFailStreak++
			n := serialFailStreak
			serialMu.Unlock()
			if n >= 2 {
				go serialWatchdog()
			}
			return false
		}
	}
}

func serialPushAllRelays() {
	st.mu.RLock()
	cp := map[string]bool{}
	for k, v := range st.Relays {
		cp[k] = v
	}
	st.mu.RUnlock()
	for id, on := range cp {
		serialPushRelay(id, on)
		time.Sleep(150 * time.Millisecond) // ponytail: stagger hindari inrush 140mA dual ON drop 5V, turun ke 80 jika 5V kuat
	}
}

// serialPowerCycleUNO matikan port USB 1-1 lalu nyalakan lagi.
// Setara cabut-colok kabel, tapi via software (butuh root).
// Dipakai saat DTR reset gagal (UNO bengong total).
// ponytail: path sysfs bisa beda port; cari otomatis via readlink, fallback 1-1.
func serialPowerCycleUNO() bool {
	// Cari direktori sysfs milik ttyACM0 otomatis, fallback 1-1.
	// Hanya device itu yang di-authorized 0/1 — port lain (mis. daya Mikrotik) tak tersentuh.
	usbdir := "/sys/bus/usb/devices/1-1"
	if link, err := filepath.EvalSymlinks("/sys/class/tty/ttyACM0/device"); err == nil {
		// link = .../1-1:1.0 → naik 1 level = .../1-1
		if dir := filepath.Dir(link); filepath.Base(dir) != "usb1" {
			usbdir = dir
		}
	}
	auth := usbdir + "/authorized"
	// pastikan ini memang UNO FIREPHX, bukan disk/modem
	prod, _ := os.ReadFile(usbdir + "/product")
	man, _ := os.ReadFile(usbdir + "/manufacturer")
	p, m := strings.TrimSpace(string(prod)), strings.TrimSpace(string(man))
	if !strings.Contains(p, "SER") && !strings.Contains(m, "FIREPHX") {
		log.Printf("serial: power-cycle batal, %s bukan UNO (%s %s)", usbdir, m, p)
		return false
	}
	log.Printf("serial: power-cycle USB %s (cabut software)...", usbdir)
	serialMu.Lock()
	if serialFile != nil {
		serialFile.Close()
		serialFile = nil
	}
	serialMu.Unlock()
	if err := os.WriteFile(auth, []byte("0"), 0644); err != nil {
		log.Printf("serial: power-cycle off gagal: %v", err)
		return false
	}
	time.Sleep(3000 * time.Millisecond)
	if err := os.WriteFile(auth, []byte("1"), 0644); err != nil {
		log.Printf("serial: power-cycle on gagal: %v", err)
		return false
	}
	log.Println("serial: power-cycle on, tunggu UNO boot...")
	time.Sleep(6000 * time.Millisecond) // enumerasi USB + bootloader + splash
	serialMu.Lock()
	serialFile = serialOpen()
	has := serialFile != nil
	serialMu.Unlock()
	if !has {
		log.Println("serial: power-cycle gagal, UNO tak muncul")
		return false
	}
	st.mu.Lock()
	st.Connected = true
	st.mu.Unlock()
	log.Println("serial: power-cycle OK, restore relay")
	serialPushOLED()
	serialPushAllRelays()
	time.Sleep(800 * time.Millisecond)
	serialPushAllRelays()
	return true
}

// serialResetUNO reset UNO tanpa cabut kabel: tutup port + buka lagi.
// Buka port toggle DTR → UNO auto-reset via bootloader; tunggu
// bootloader+splash (~4s) lalu dorong ulang semua relay dari state (persist kv).
// ponytail: sleep blocking 5s di handler HTTP, pindah async jika WriteTimeout diketatkan.
func serialResetUNO() bool {
	b, _ := json.Marshal(map[string]string{"cmd": "reset"})
	serialWriteLine(string(b)) // coba soft reset dulu (butuh sketch baru); gagal pun lanjut DTR reset
	serialMu.Lock()
	if serialFile != nil {
		serialFile.Close()
		serialFile = nil
	}
	serialMu.Unlock()
	log.Println("serial: reset UNO, tunggu boot...")
	time.Sleep(4000 * time.Millisecond) // bootloader ~2s + splash 1.8s
	serialMu.Lock()
	serialFile = serialOpen()
	has := serialFile != nil
	serialMu.Unlock()
	if !has {
		log.Println("serial: reset gagal, UNO tak muncul")
		st.mu.Lock()
		st.Connected = false
		st.mu.Unlock()
		return false
	}
	st.mu.Lock()
	st.Connected = true
	st.mu.Unlock()
	log.Println("serial: UNO reset OK, restore relay")
	serialPushOLED()
	serialPushAllRelays()
	time.Sleep(800 * time.Millisecond)
	serialPushAllRelays() // retry 2 — pastikan sampai setelah splash
	return true
}

// serialResetUNOHard: DTR reset dulu; kalau masih bisu (tak ada ack),
// lanjut power-cycle USB = setara cabut-colok, via software.
func serialResetUNOHard() (string, bool) {
	if serialResetUNO() {
		// verifikasi: dorong relay lamp sesuai state, wajib ACK
		st.mu.RLock()
		want := st.Relays["lamp"]
		st.mu.RUnlock()
		if serialPushRelay("lamp", want) {
			return "dtr", true
		}
		log.Println("serial: DTR reset tapi UNO bisu, lanjut power-cycle")
	}
	if serialPowerCycleUNO() {
		return "powercycle", true
	}
	return "gagal", false
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
			// ack UNO: {"ack":"lamp","on":1} — bukti relay fisik gerak
			if rawID, ok := m["ack"]; ok {
				var id string
				var onInt int
				_ = json.Unmarshal(rawID, &id)
				if rawOn, ok2 := m["on"]; ok2 {
					_ = json.Unmarshal(rawOn, &onInt)
				}
				select {
				case serialAckCh <- serialAck{id: id, on: onInt == 1}:
				default:
				}
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
		// scanner berhenti = port putus; reset agar reconnect.
		// ponytail: cek identitas fd — jangan bunuh koneksi baru milik writer.
		serialMu.Lock()
		if serialFile != nil && serialFile == f {
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
		// ponytail: push awal 1.5s biar UNO siap setelah splash; tanpa ini OLED blank sampai PollInterval
		go func() {
			time.Sleep(3500 * time.Millisecond)
			st.mu.RLock()
			on := st.OLEDOn
			always := st.ClockAlwaysOn
			st.mu.RUnlock()
			if !on {
				serialPushOLED() // kirim 4 baris kosong = mati
			} else if always {
				serialPushClockAlways(true)
			} else {
				serialPushOLED()
			}
			st.mu.RLock(); cpLog := map[string]bool{}; for k,v := range st.Relays { cpLog[k]=v }; onLog := st.OLEDOn; st.mu.RUnlock(); log.Printf("serial: boot push relays %v oled_on=%v", cpLog, onLog)
			serialPushAllRelays()
			time.Sleep(800 * time.Millisecond)
			serialPushAllRelays() // retry 2 — bootloader+splash 2.3s kadang telan push pertama
			time.Sleep(800 * time.Millisecond)
			serialPushAllRelays() // retry 3 — pastikan sampai
		}()
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
			missing := true
			for _, dev := range serialCandidates {
				if _, err := os.Stat(dev); err == nil {
					missing = false
					break
				}
			}
			if missing {
				serialMu.Lock()
				if serialFile != nil {
					log.Println("serial: device hilang, reset koneksi")
					serialFile.Close()
					serialFile = nil
				}
				serialMu.Unlock()
				st.mu.Lock()
				st.Connected = false
				st.mu.Unlock()
			} else {
				continue
			}
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
			time.Sleep(500 * time.Millisecond)
			st.mu.RLock()
			on := st.OLEDOn
			always := st.ClockAlwaysOn
			st.mu.RUnlock()
			if !on {
				serialPushOLED()
			} else if always {
				serialPushClockAlways(true)
			} else {
				serialPushOLED()
			}
			serialPushAllRelays()
		}
	}
}
