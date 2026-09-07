package main

import (
	"database/sql"
	"time"
)

// energy daily Wh -> state kWh/Rp, no extra dep.
// ponytail: tick 60s (~1m granularity), upgrade ke per-second jika butuh presisi tagihan.

var wib = time.FixedZone("WIB", 7*3600)

func energyLoop() {
	updateEnergyState()
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	last := time.Now()
	for range tick.C {
		now := time.Now()
		dt := now.Sub(last).Seconds()
		last = now
		if dt <= 0 || dt > 120 {
			dt = 60
		}
		// watt dari settings
		settings.mu.RLock()
		wLamp := settings.EnergyWattLamp
		wFan := settings.EnergyWattFan
		settings.mu.RUnlock()
		st.mu.RLock()
		lampOn := st.RelayLamp
		if st.Relays != nil {
			if v, ok := st.Relays["lamp"]; ok {
				lampOn = v
			}
		}
		fanOn := st.RelayFan
		if st.Relays != nil {
			if v, ok := st.Relays["fan"]; ok {
				fanOn = v
			}
		}
		st.mu.RUnlock()
		whLamp := 0.0
		whFan := 0.0
		if lampOn {
			whLamp = float64(wLamp) * dt / 3600
		}
		if fanOn {
			whFan = float64(wFan) * dt / 3600
		}
		if whLamp == 0 && whFan == 0 {
			updateEnergyState()
			continue
		}
		date := now.In(wib).Format("2006-01-02")
		if db != nil {
			db.Exec(`INSERT INTO energy_daily(date, wh_lamp, wh_fan) VALUES(?,?,?) ON CONFLICT(date) DO UPDATE SET wh_lamp=wh_lamp+excluded.wh_lamp, wh_fan=wh_fan+excluded.wh_fan`, date, whLamp, whFan)
		}
		updateEnergyState()
	}
}

func updateEnergyState() {
	if db == nil {
		return
	}
	now := time.Now().In(wib)
	today := now.Format("2006-01-02")
	monthStart := now.Format("2006-01-01")
	var todayWh sql.NullFloat64
	db.QueryRow(`SELECT wh_lamp+wh_fan FROM energy_daily WHERE date=?`, today).Scan(&todayWh)
	var monthWh sql.NullFloat64
	db.QueryRow(`SELECT SUM(wh_lamp+wh_fan) FROM energy_daily WHERE date>=?`, monthStart).Scan(&monthWh)
	tWh := 0.0
	if todayWh.Valid {
		tWh = todayWh.Float64
	}
	mWh := 0.0
	if monthWh.Valid {
		mWh = monthWh.Float64
	}
	settings.mu.RLock()
	tariff := settings.EnergyTariff
	settings.mu.RUnlock()
	if tariff <= 0 {
		tariff = 1444
	}
	tKwh := tWh / 1000
	mKwh := mWh / 1000
	tCost := int(tKwh * float64(tariff) + 0.5)
	mCost := int(mKwh * float64(tariff) + 0.5)
	st.mu.Lock()
	st.EnergyTodayKwh = tKwh
	st.EnergyMonthKwh = mKwh
	st.EnergyCostToday = tCost
	st.EnergyCostMonth = mCost
	st.mu.Unlock()
}
