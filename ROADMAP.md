# aqua — Web Monitor & Control Arduino (UNO + OLED)

Web dashboard buat aquarium: monitor sensor + kontrol relay (lampu/kipas) + kirim teks ke OLED + log + jadwal via cron-job.com eksternal.

Arsitektur: **STB (Armbian) = otak** (Go server, baca serial UNO, web dashboard). **UNO = I/O node** (baca sensor → kirim JSON ke STB; terima teks dari STB → tampil OLED). UNO tanpa logika data.

Deploy: Oracle = dev only (port 30221) → push GitHub → user git pull di STB (prod).

## Fase 0 — Skeleton Go + tema ✨
- Repo `aqua`, Go server :30221, go:embed static
- `GET /api/health`, serve index.html
- Tema fresh: light (putih) + dark (hitam), accent aqua/teal, monospace, responsive 320/375/414/768
- **Selesai kalau:** curl /api/health 200, browser buka index render, toggle tema jalan, gak ada JS error

## Fase 1 — State + Monitor
- Goroutine serial reader: buka `/dev/ttyACM0` (fallback ttyUSB0), parse JSON one-line dari UNO, simpan ke state memory
- Mode mock: tanpa UNO, data simulasi (biar web kebukti duluan sebelum hardware datang)
- Monitor: kartu stat suhu/arus/tegangan, status UNO connected/disconnected, polling 5 detik
- **Selesai kalau:** /api/state balas JSON sensor; kartu update di browser (mock + serial)

## Fase 2 — Control + OLED + Log
- Toggle relay lampu/kipas (`POST /api/relay`) → kirim perintah ke UNO via serial
- Kirim teks ke OLED (`POST /api/oled`)
- Settings: atur isi OLED (line 1-4), tema, polling interval → simpan `config.json`
- Log: akses web (IP/sumber/waktu) + aksi (tombol, relay, oled, error) → ring buffer file
- **Selesai kalau:** toggle relay + kirim OLED dari browser, log tercatat, config tersimpan

## Fase 3 — Jadwal via cron-job.com ✅
- Endpoint GET `/api/cron/{token}/relay/{lamp,fan}/on|off` buat cron eksternal (token rahasia auto-generate)
- Halaman Jadwal: tambah/aktif/nonaktif/hapus jadwal langsung dari web (via cron-job.org REST API, API key di Settings)
- Semua hit cron tercatat di log sebagai aksi "auto"
- Rollback: jadwal yang gagal dibuat di cron-job.org gak disimpan
- **Selesai kalau:** tambah jadwal → job kepush ke cron-job.org; curl endpoint cron eksekusi + muncul di log; hapus → job kehapus remote

## Fase 4 — Login PIN 4 digit ✅
- Overlay login PIN 4 angka (`POST /api/login` → cookie `aqua_session` HttpOnly 24h), middleware `authMW` proteksi /api/* (kecuali /health & /api/cron/*)
- Ganti PIN dari Settings (`POST /api/pin`, validasi 4 digit numerik), simpan `config.json`, log "ganti PIN"
- Logout `POST /api/logout` hapus session + cookie
- Auto-skip overlay jika cookie masih valid (`GET /api/state` 200)
- **Selesai kalau:** tanpa login /api/state 401; login PIN salah 401; login benar → state 200; ganti PIN → login lama gagal, baru ok; cron & health tetap publik
