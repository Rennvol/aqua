package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func dbInit() {
	var err error
	db, err = sql.Open("sqlite", "file:santra.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal("db open:", err)
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT)`,
		`CREATE TABLE IF NOT EXISTS relays(id TEXT PRIMARY KEY, label TEXT, icon TEXT)`,
		`CREATE TABLE IF NOT EXISTS sensors(id TEXT PRIMARY KEY, label TEXT, unit TEXT, icon TEXT)`,
		`CREATE TABLE IF NOT EXISTS schedules(id INTEGER PRIMARY KEY, relay TEXT, state TEXT, hour INTEGER, minute INTEGER, enabled INTEGER, cron_job_id INTEGER)`,
		`CREATE TABLE IF NOT EXISTS history(t INTEGER PRIMARY KEY, temp REAL, power REAL, volt REAL, curr REAL)`,
		`CREATE TABLE IF NOT EXISTS logs(time TEXT, ip TEXT, action TEXT)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			log.Fatal("migrate:", err, s)
		}
	}
	db.Exec(`ALTER TABLE schedules ADD COLUMN hit_count INTEGER DEFAULT 0`)
	migrateFromJSON()
	syncSettingsFromDB()
}

func kvGet(k, fallback string) string {
	var v string
	if err := db.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v); err == nil {
		return v
	}
	return fallback
}
func kvSet(k, v string) {
	db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
}

func migrateFromJSON() {
	// if kv already has pin, assume migrated
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM kv`).Scan(&cnt)
	if cnt > 0 {
		return
	}
	// load config.json if exists
	b, err := os.ReadFile("config.json")
	if err != nil {
		// seed defaults
		seedDefaults()
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		seedDefaults()
		return
	}
	// kv fields
	for _, k := range []string{"pin", "oled_line1", "oled_line2", "oled_line3", "oled_line4", "oled_line1r", "oled_line2r", "oled_line3r", "oled_line4r", "poll_interval", "cron_api_key", "cron_token", "public_url"} {
		if v, ok := raw[k]; ok {
			var s string
			// try string, else number -> string
			if json.Unmarshal(v, &s) == nil {
				kvSet(k, s)
			} else {
				kvSet(k, string(v))
			}
		}
	}
	// relays
	if v, ok := raw["relays"]; ok {
		var rs []RelayDef
		if json.Unmarshal(v, &rs) == nil {
			for _, r := range rs {
				db.Exec(`INSERT OR IGNORE INTO relays(id,label,icon) VALUES(?,?,?)`, r.ID, r.Label, r.Icon)
			}
		}
	}
	// sensors
	if v, ok := raw["sensors"]; ok {
		var ss []SensorDef
		if json.Unmarshal(v, &ss) == nil {
			for _, s := range ss {
				db.Exec(`INSERT OR IGNORE INTO sensors(id,label,unit,icon) VALUES(?,?,?,?)`, s.ID, s.Label, s.Unit, s.Icon)
			}
		}
	}
	// schedules
	if v, ok := raw["schedules"]; ok {
		var sc []Schedule
		if json.Unmarshal(v, &sc) == nil {
			for _, s := range sc {
				en := 0
				if s.Enabled {
					en = 1
				}
				db.Exec(`INSERT OR IGNORE INTO schedules(id,relay,state,hour,minute,enabled,cron_job_id) VALUES(?,?,?,?,?,?,?)`, s.ID, s.Relay, s.State, s.Hour, s.Minute, en, s.CronJobID)
			}
		}
	}
	// history/logs jsonl -> db (best-effort, ignore errors)
	if b, err := os.ReadFile("history.jsonl"); err == nil {
		for _, line := range splitLines(string(b)) {
			if line == "" {
				continue
			}
			var p Point
			if json.Unmarshal([]byte(line), &p) == nil {
				db.Exec(`INSERT OR IGNORE INTO history(t,temp,power,volt,curr) VALUES(?,?,?,?,?)`, p.T, p.Temp, p.Power, p.Volt, p.Curr)
			}
		}
	}
	if b, err := os.ReadFile("logs.jsonl"); err == nil {
		for _, line := range splitLines(string(b)) {
			if line == "" {
				continue
			}
			var e LogEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				db.Exec(`INSERT INTO logs(time,ip,action) VALUES(?,?,?)`, e.Time, e.IP, e.Action)
			}
		}
	}
	// ensure defaults if still empty
	seedDefaults()
	log.Println("migrated config.json -> santra.db")
}

func syncSettingsFromDB() {
	if db == nil {
		return
	}
	// load kv into global settings
	if v := kvGet("pin", ""); v != "" {
		settings.Pin = v
	}
	if v := kvGet("oled_line1", ""); v != "" {
		settings.OLEDLine1 = v
	}
	if v := kvGet("oled_line2", ""); v != "" {
		settings.OLEDLine2 = v
	}
	if v := kvGet("oled_line3", ""); v != "" {
		settings.OLEDLine3 = v
	}
	if v := kvGet("oled_line4", ""); v != "" {
		settings.OLEDLine4 = v
	}
	for i, k := range []string{"oled_line1r", "oled_line2r", "oled_line3r", "oled_line4r"} {
		if v := kvGet(k, "\x00"); v != "\x00" {
			switch i {
			case 0:
				settings.OLEDLine1R = v
			case 1:
				settings.OLEDLine2R = v
			case 2:
				settings.OLEDLine3R = v
			case 3:
				settings.OLEDLine4R = v
			}
		}
	}
	if v := kvGet("poll_interval", ""); v != "" {
		var iv int
		for _, c := range v { if c >= '0' && c <= '9' { iv = iv*10 + int(c-'0') } }
		if iv > 0 { settings.PollInterval = iv }
	}
	if v := kvGet("cron_api_key", ""); true {
		settings.CronAPIKey = v
	}
	if v := kvGet("cron_token", ""); true {
		settings.CronToken = v
	}
	if v := kvGet("public_url", ""); true {
		settings.PublicURL = v
	}
	// relays
	rows, _ := db.Query(`SELECT id,label,icon FROM relays ORDER BY id`)
	if rows != nil {
		var rs []RelayDef
		for rows.Next() {
			var r RelayDef
			rows.Scan(&r.ID, &r.Label, &r.Icon)
			rs = append(rs, r)
		}
		rows.Close()
		if len(rs) > 0 {
			settings.Relays = rs
		}
	}
	rows, _ = db.Query(`SELECT id,label,unit,icon FROM sensors ORDER BY id`)
	if rows != nil {
		var ss []SensorDef
		for rows.Next() {
			var s SensorDef
			rows.Scan(&s.ID, &s.Label, &s.Unit, &s.Icon)
			ss = append(ss, s)
		}
		rows.Close()
		if len(ss) > 0 {
			settings.Sensors = ss
		}
	}
	// schedules
	rows, _ = db.Query(`SELECT id,relay,state,hour,minute,enabled,cron_job_id,hit_count FROM schedules ORDER BY id`)
	if rows != nil {
		var sc []Schedule
		for rows.Next() {
			var s Schedule
			var en int
			rows.Scan(&s.ID, &s.Relay, &s.State, &s.Hour, &s.Minute, &en, &s.CronJobID, &s.HitCount)
			s.Enabled = en != 0
			sc = append(sc, s)
		}
		rows.Close()
		settings.Schedules = sc
	}
	// init state maps from defs
	st.mu.Lock()
	if st.Relays == nil {
		st.Relays = map[string]bool{}
	}
	for _, r := range settings.Relays {
		if _, ok := st.Relays[r.ID]; !ok {
			st.Relays[r.ID] = false
		}
	}
	if st.Sensors == nil {
		st.Sensors = map[string]float64{}
	}
	for _, s := range settings.Sensors {
		if _, ok := st.Sensors[s.ID]; !ok {
			st.Sensors[s.ID] = 0
		}
	}
	st.mu.Unlock()
}


func seedDefaults() {
	var c int
	db.QueryRow(`SELECT COUNT(*) FROM relays`).Scan(&c)
	if c == 0 {
		db.Exec(`INSERT OR IGNORE INTO relays(id,label,icon) VALUES('lamp','Lampu','💡'),('fan','Kipas','🌬️')`)
	}
	db.QueryRow(`SELECT COUNT(*) FROM sensors`).Scan(&c)
	if c == 0 {
		db.Exec(`INSERT OR IGNORE INTO sensors(id,label,unit,icon) VALUES('temp','Suhu Air','°C','🌡️'),('voltage','Tegangan','V','⚡'),('current','Arus','A','🔌'),('power','Daya','W','🔋')`)
	}
	if kvGet("pin", "") == "" {
		kvSet("pin", "1234")
	}
	if kvGet("oled_line1", "") == "" {
		kvSet("oled_line1", "temp")
		kvSet("oled_line2", "current")
		kvSet("oled_line3", "voltage")
		kvSet("oled_line4", "relay")
	}
	if kvGet("poll_interval", "") == "" {
		kvSet("poll_interval", "5")
	}
}
