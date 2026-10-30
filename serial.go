package main

import (
	"bufio"
	"encoding/json"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"golang.org/x/sys/unix"
)

// Serial link STB <-> UNO, 9600 baud, 1 baris JSON + \n.
// ponytail: baud diset via `stty` sekali saat open, bukan lib serial;
// upgrade ke lib (go.bug.st/serial) kalau butuh auto-reconnect canggih.
var (
	serialMu   sync.Mutex
	serialFile *os.File
	// serialFailStreak hitung gagal beruntun (tulis gagal / tanpa ack);
	// watchdog power-cycle UNO saat >= 2.
	// int32 + atomic: dibaca/ditulis dari readLoop, pushRelay, watchdog
	// yang jalan di goroutine beda. int biasa + mutex parsial = race.
	serialFailStreak int32
	// serialAckCh terima ack UNO {"ack":"lamp","on":1}; push relay tunggu ini.
	serialAckCh = make(chan serialAck, 8)
	// serialAckMu gilirkan tulis+tunggu-ack biar ack tak tertukar antar relay.
	serialAckMu sync.Mutex
	// serialWatchdogAt cooldown: jangan hard reset tiap push gagal.
	// Tanpa ini 10 jadwal gagal = 10 reset beruntun, USB naik-turun terus.
	// 300s: power-cycle software 15:53→15:54 bikin enumerasi USB gagal
	// (error -71, unable to enumerate) sampai /dev/ttyACM* hilang total.
	// Reset agresif = bumerang: UNO goyah di level listrik, bukan sketch.
	serialWatchdogAt int64
	// serialPowerCycleAt cooldown khusus power-cycle USB (15 menit):
	// cabut-colok software berisiko bikin kernel tak bisa enum ulang.
	// DTR reset boleh tiap 5 mnt, power-cycle hanya kalau DTR gagal
	// beruntun + jeda 15 mnt terlampaui.
	serialPowerCycleAt int64
)

// serialAck pengakuan UNO bahwa relay fisik sudah digerakkan. Untuk pong
// (heartbeat) lamp/fan membawa state relay yang dipegang UNO.
type serialAck struct {
	id   string
	on   bool
	lamp bool
	fan  bool
}

var serialCandidates = []string{"/dev/ttyACM0", "/dev/ttyACM1", "/dev/ttyUSB0"}

// serialFindDevices cari semua port serial USB yang ada, urut stabil.
// ACM0 duluan bila ada (jalur utama), sisanya ACM1..n lalu USB.
// Tanpa ini, UNO yang pindah ACM0->ACM1 (power-cycle/enumerasi ulang)
// tak pernah ditemukan: open cengkram ACM0 mati, tulis ditelan CDC-ACM,
// relay diam + TAK ADA ACK selamanya sampai restart manual.
func serialFindDevices() []string {
	found := []string{}
	for _, pat := range []string{"/dev/ttyACM*", "/dev/ttyUSB*"} {
		ms, _ := filepath.Glob(pat)
		for _, d := range ms {
			// Wajib char device + ada di sysfs: echo ke port yang sedang
			// hilang meninggalkan file biasa /dev/ttyACM1 — open+write ke
			// file selalu "sukses" tapi UNO tak terima, ACK mati selamanya.
			fi, err := os.Stat(d)
			if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
				continue
			}
			if _, err := os.Stat("/sys/class/tty/" + filepath.Base(d)); err != nil {
				continue
			}
			found = append(found, d)
		}
	}
	sort.Strings(found)
	// ACM0 tetap prioritas pertama bila ada
	for i, d := range found {
		if d == "/dev/ttyACM0" && i != 0 {
			found[0], found[i] = found[i], found[0]
			break
		}
	}
	if len(found) == 0 {
		found = serialCandidates
	}
	return found
}

// serialOpen buka port pertama yang ada. Return nil kalau tak ada (caller fallback mock).
func serialOpen() *os.File {
	for _, dev := range serialFindDevices() {
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		// set baud 9600 raw via stty (stdlib tak bisa set baud sendiri).
		// Tanpa -hupcl: DTR ikut turun saat port ditutup = auto-reset hardware
		// jalan tiap close/open.
		_ = exec.Command("stty", "-F", dev, "9600", "raw", "-echo").Run()
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
			atomic.AddInt32(&serialFailStreak, 1)
			go serialWatchdog()
			return false
		}
		serialFile.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err2 := serialFile.WriteString(s + "\n"); err2 != nil {
			log.Printf("serial: retry gagal: %v", err2)
			serialFile.Close()
			serialFile = nil
			atomic.AddInt32(&serialFailStreak, 1)
			go serialWatchdog()
			return false
		}
	}
	// tulis OK != UNO hidup: kernel buffer CDC-ACM selalu terima walau
	// firmware hang. Jangan reset streak di sini — hanya ACK valid yang
	// boleh nol-kan (lihat serialPushRelay). Kalau tidak, streak tak pernah
	// capai 2 dan watchdog tak pernah jalan: inilah kenapa UNO bengong
	// selamanya sampai tombol fisik ditekan.
	return true
}

// serialWatchdog: 2x gagal beruntun = UNO hang → hard reset sekali
// (DTR, gagal lanjut power-cycle USB), lalu restore relay. Async.
func serialWatchdog() {
	n := atomic.LoadInt32(&serialFailStreak)
	if n < 2 {
		return
	}
	// cooldown 300s: reset terakhir < 5 menit lalu = skip. Hard reset butuh
	// ~15s (DTR 4s / power-cycle 9s); tanpa jeda, push jadwal berikutnya
	// picu reset lagi sebelum UNO selesai boot → reset bertumpuk, USB
	// pindah ACM0→ACM1→ACM2 terus. 60s TERBUKTI kurang: log 15:53
	// watchdog→DTR→power-cycle→enum gagal→16:00 USB hilang total.
	now := time.Now().Unix()
	if last := atomic.LoadInt64(&serialWatchdogAt); now-last < 300 {
		return
	}
	atomic.StoreInt64(&serialWatchdogAt, now)
	log.Printf("serial: watchdog %d gagal beruntun → hard reset UNO", n)
	if _, ok := serialResetUNOHard(); ok {
		atomic.StoreInt32(&serialFailStreak, 0)
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
// Coba 2x dalam satu kunci giliran: miss tunggal (ack basi/tabrak
// boot-push/splash telan) sembuh sendiri tanpa watchdog. Return false =
// relay BELUM tentu gerak (web harus tampilkan gagal).
func serialPushRelay(id string, on bool) bool {
	serialAckMu.Lock()
	defer serialAckMu.Unlock()
	v := 0
	if on {
		v = 1
	}
	b, _ := json.Marshal(map[string]interface{}{"cmd": "relay", "id": id, "on": v})
	for attempt := 0; attempt < 2; attempt++ {
		// kuras ack basi biar tak salah baca ack perintah lama
	drain:
		for {
			select {
			case <-serialAckCh:
			default:
				break drain
			}
		}
		if !serialWriteLine(string(b)) {
			log.Printf("serial: relay %s -> %d GAGAL tulis (UNO tak terjangkau)", id, v)
			return false
		}
		// tunggu ack UNO max 4s; tanpa ack = relay diam walau tulis "sukses".
		// 2.5s terlalu mepet setelah reset: bootloader ~2s + splash 1.8s
		// telan push pertama (log 15:46:08 & 16:05:30 selalu miss pertama).
		timeout := time.After(4000 * time.Millisecond)
	wait:
		for {
			select {
			case a := <-serialAckCh:
				if a.id == id {
					if a.on == on {
						atomic.StoreInt32(&serialFailStreak, 0)
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
				if attempt == 0 {
					log.Printf("serial: relay %s -> %d miss 1x, coba lagi", id, v)
					time.Sleep(300 * time.Millisecond)
					break wait // keluar wait-loop, lanjut attempt 2
				}
				log.Printf("serial: relay %s -> %d TAK ADA ACK (relay diam!)", id, v)
				if atomic.AddInt32(&serialFailStreak, 1) >= 2 {
					go serialWatchdog()
				}
				return false
			}
		}
	}
	return false
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

// serialPulseDTR kirim pulse DTR 250ms ke UNO via ioctl TIOCM — ini yang
// picu auto-reset hardware via kapasitor DTR→RESET. Tutup+buka port saja
// TIDAK cukup saat stty -hupcl (DTR bertahan, UNO tak reboot — log
// "DTR reset tapi UNO bisu" kemarin). Return false = fd mati, lanjut
// power-cycle.
func serialPulseDTR() bool {
	serialMu.Lock()
	f := serialFile
	serialMu.Unlock()
	if f == nil {
		return false
	}
	fd := int(f.Fd())
	m, err := unix.IoctlGetInt(fd, unix.TIOCMGET)
	if err != nil {
		return false
	}
	// DTR turun 250ms lalu naik = falling edge reset
	if err := unix.IoctlSetInt(fd, unix.TIOCMSET, m&^unix.TIOCM_DTR); err != nil {
		return false
	}
	time.Sleep(250 * time.Millisecond)
	if err := unix.IoctlSetInt(fd, unix.TIOCMSET, m|unix.TIOCM_DTR); err != nil {
		return false
	}
	return true
}

// serialResetUNO reset UNO tanpa cabut kabel: pulse DTR eksplisit (auto-reset
// hardware), tunggu bootloader+splash (~4s), lalu dorong ulang semua relay
// dari state (persist kv).
// ponytail: sleep blocking 5s di handler HTTP, pindah async jika WriteTimeout diketatkan.
func serialResetUNO() bool {
	b, _ := json.Marshal(map[string]string{"cmd": "reset"})
	serialWriteLine(string(b)) // coba soft reset dulu (butuh sketch baru); gagal pun lanjut DTR reset
	serialPulseDTR()           // gagal pun lanjut: tutup+buka di bawah tetap coba
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
// lanjut power-cycle USB = setara cabut-colok, via software — TAPI
// hanya jika cooldown 15 menit terlampaui. Power-cycle 15:53 bikin
// enumerasi USB gagal total (error -71) sampai ttyACM* hilang 16:00.
// Tanpa gate ini, watchdog agresif = bumerang listrik.
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
	now := time.Now().Unix()
	if last := atomic.LoadInt64(&serialPowerCycleAt); now-last < 900 {
		log.Printf("serial: power-cycle skip (cooldown 15 mnt, sisa %ds)", 900-(now-last))
		return "gagal", false
	}
	atomic.StoreInt64(&serialPowerCycleAt, now)
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
				// pong membawa state relay UNO (heartbeat)
				var lampInt, fanInt int
				if rawLamp, ok3 := m["lamp"]; ok3 {
					_ = json.Unmarshal(rawLamp, &lampInt)
				}
				if rawFan, ok4 := m["fan"]; ok4 {
					_ = json.Unmarshal(rawFan, &fanInt)
				}
				select {
				case serialAckCh <- serialAck{id: id, on: onInt == 1, lamp: lampInt == 1, fan: fanInt == 1}:
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
//
// PENTING: fungsi ini tidak boleh menulis Mode/Connected. Dulu ia menyetel
// Mode="mock" + Connected=false di awal, padahal RunSerial memanggilnya tanpa
// syarat di akhir — jadi setiap boot state berakhir "mock" walau UNO benar-benar
// tersambung, dan /api/state berbohong (connected:false) sementara relay dapat
// ACK. Kepemilikan Mode/Connected ada di RunSerial/watchSerialPresence.
func RunMock() {
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
		st.mu.Lock()
		st.Mode = "mock"
		st.Connected = false
		st.mu.Unlock()
		log.Println("serial: /dev/ttyACM0 tak ada, mode mock")
	}
	go serialReadLoop()
	go serialOLEDLoop()
	go serialClockAlwaysLoop()
	go watchSerialPresence()
	go serialHeartbeatLoop()
	RunMock() // tetap jalan sebagai fallback sensor; diam saat serial aktif
}

// serialHeartbeatLoop kirim ping tiap 5 detik dan, saat UNO menjawab pong dengan
// state relay yang berbeda dari yang dipegang STB, push ulang relay yang
// menyimpang. Ini menutup celah yang tidak tertangkap watchSerialPresence: UNO
// reset sendiri (watchdog/DTR/brownout saat mati lampu) sementara fd serial di
// STB masih valid — dulu relay tinggal OFF sampai jadwal berikutnya.
//
// ponytail: ping 5 dtk, butuh 2 pong berturut-turut yang menyimpang sebelum
// menindak, supaya satu balasan basi tidak memicu push sia-sia. Naikkan
// interval kalau lalu lintas serial jadi masalah.
func serialHeartbeatLoop() {
	time.Sleep(6 * time.Second) // beri waktu push awal selesai
	divergent := 0
	for {
		time.Sleep(5 * time.Second)
		serialMu.Lock()
		has := serialFile != nil
		serialMu.Unlock()
		if !has {
			divergent = 0
			continue
		}
		_, lamp, fan, ok := serialPing()
		if !ok {
			divergent = 0
			continue
		}
		// UNO baru saja reset: state-nya belum tentu sama dengan STB.
		st.mu.RLock()
		wantLamp, wantFan := st.RelayLamp, st.RelayFan
		st.mu.RUnlock()
		if lamp == wantLamp && fan == wantFan {
			divergent = 0
			continue
		}
		divergent++
		if divergent < 2 {
			continue
		}
		log.Printf("serial: UNO state menyimpang (lamp %v→%v, fan %v→%v), push ulang",
			lamp, wantLamp, fan, wantFan)
		divergent = 0
		serialPushAllRelays()
	}
}

// serialPing kirim {"cmd":"ping"} dan tunggu pong berisi state relay UNO.
func serialPing() (got bool, lamp, fan bool, ok bool) {
	serialAckMu.Lock()
	defer serialAckMu.Unlock()
	// buang ack basi supaya pong lama tidak dibaca sebagai jawaban sekarang
drainPing:
	for {
		select {
		case <-serialAckCh:
		default:
			break drainPing
		}
	}
	if !serialWriteLine(`{"cmd":"ping"}`) {
		return false, false, false, false
	}
	deadline := time.After(2500 * time.Millisecond)
	for {
		select {
		case a := <-serialAckCh:
			if a.id == "pong" {
				return true, a.lamp, a.fan, true
			}
		case <-deadline:
			return false, false, false, false
		}
	}
}

// watchSerialPresence pantau colok/cabut UNO tanpa restart.
func watchSerialPresence() {
	for {
		time.Sleep(3 * time.Second)
		serialMu.Lock()
		has := serialFile != nil
		serialMu.Unlock()
		if has {
			serialMu.Lock()
			cur := ""
			if serialFile != nil {
				cur = serialFile.Name()
			}
			serialMu.Unlock()
			// cek port yang sedang dipegang, bukan daftar kandidat:
			// UNO pindah ACM0->ACM1 = ACM0 hilang walau ACM1 ada.
			// Kode lama cek "ada kandidat apapun" -> missing=false -> continue
			// selamanya cengkram fd mati. Tulis ditelan CDC-ACM, TAK ADA ACK.
			if cur != "" {
				if _, err := os.Stat(cur); err != nil {
					serialMu.Lock()
					if serialFile != nil && serialFile.Name() == cur {
						log.Printf("serial: device %s hilang, reset koneksi", cur)
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
			// settle 4s: bootloader ~2s + splash 1.8s telan push pertama
			// (log 15:46:08 & 16:05:30 selalu miss push ke-1). Tanpa jeda,
			// reconnect-push tabrakan window boot = TAK ADA ACK palsu.
			time.Sleep(4000 * time.Millisecond)
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
