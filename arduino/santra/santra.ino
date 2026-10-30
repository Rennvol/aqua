// santra UNO firmware — standby terima JSON dari STB via Serial 9600.
// Protokol (1 baris JSON + \n, 9600 baud):
//   STB -> UNO : {"cmd":"oled","lines":["a","b","c","d"]}  tampil 4 baris
//   STB -> UNO : {"cmd":"clock","text":"12:34","dur":10}   jam besar 10 dtk
//   STB -> UNO : {"cmd":"clockAlways","on":1,"epoch":1710000000} jam terus self-tick + tanggal WIB
//   STB -> UNO : {"cmd":"sync","epoch":1710000000}         sync epoch UTC
//   STB -> UNO : {"cmd":"relay","id":"lamp","on":1}
//   STB -> UNO : {"cmd":"reset"}  soft reset (tombol Reset UNO di web, tanpa cabut kabel)
// Splash boot = "santra." (bukan random).
// Anti burn-in: micro-shift 2px tiap refresh + jam digit selalu beda.
// Libs: U8x8 (teks saja), ArduinoJson (v7 JsonDocument)
//
// U8x8 bukan U8g2: firmware ini tidak pernah menggambar grafik/ikon, hanya teks,
// jadi jalur gambar-penuh U8g2 (10 KB flash) dibuang dan diganti u8x8 (font
// bitmap siap pakai, tanpa dua buffer). Hemat ~8 KB flash untuk fitur baru.
// Konsekuensi: posisi karakter terkunci di grid font (kolom x lebar sel), tidak
// ada lagi teks 32px bebas posisi. Lihat showClockFromEpoch() untuk layout.

#include <U8x8lib.h>
#include <ArduinoJson.h>
#include <Wire.h>
#include <avr/wdt.h>

// HW I2C = pin A4/A5 (sesuai wiring board), bukan software I2C.
U8X8_SSD1306_128X64_NONAME_HW_I2C u8x8(U8X8_PIN_NONE);

// Baris teks: 8x13B_1x2 adalah font tebal 16x26 px (lebar sel 8, 1x2 = dobel
// tinggi), jadi 128/16 = 8 kolom x 64/26 = 2 baris. Dipakai untuk jam besar.
// 6x10_1x2 untuk 4 baris status (lebar sel 6, 2 baris tinggi = 20 px).

// Tulis teks di kolom karakter, bukan piksel: rata tengah manual. Jumlah kolom
// diambil dari u8x8.getCols() setelah setFont, jadi tidak ada angka ajaib lebar
// sel yang bisa salah kalau fontnya diganti.
static void drawCentered(uint8_t row, const char *t, uint8_t dx = 0){
  uint8_t cols = u8x8.getCols();
  uint8_t n = strlen(t);
  uint8_t col = n < cols ? (cols - n) / 2 + dx : dx;
  u8x8.drawString(col, row, t);
}

String lines[4] = {"santra.", "pantau & kontrol", "menunggu STB...", ""};
bool lampOn = false;
bool fanOn = false;
unsigned long clockRestoreAt = 0;
uint8_t burnShift = 0; // 0/2 px micro-shift anti burn-in

// --- software RTC + optional DS3231 ---
bool hasRTC = false;
bool clockAlwaysMode = false;
uint32_t softEpoch = 0;
unsigned long softMillis = 0;
uint32_t lastMinute = 0xFFFFFFFF;

static uint8_t bcd2dec(uint8_t v){ return (v>>4)*10 + (v & 0x0F); }
static uint8_t dec2bcd(uint8_t v){ return ((v/10)<<4)|(v%10); }
bool rtcProbe(){ Wire.beginTransmission(0x68); return Wire.endTransmission()==0; }
void rtcSet(uint32_t epoch){
  uint32_t s = epoch % 60; uint32_t m = (epoch/60)%60; uint32_t h = (epoch/3600)%24;
  Wire.beginTransmission(0x68);
  Wire.write(0x00);
  Wire.write(dec2bcd(s)); Wire.write(dec2bcd(m)); Wire.write(dec2bcd(h));
  Wire.write(1); Wire.write(dec2bcd(1)); Wire.write(dec2bcd(1)); Wire.write(dec2bcd(24));
  Wire.endTransmission();
}
uint32_t rtcGet(){
  Wire.beginTransmission(0x68); Wire.write(0x00); Wire.endTransmission();
  Wire.requestFrom((int)0x68, 3);
  if(Wire.available()<3) return 0;
  uint8_t ss=bcd2dec(Wire.read() & 0x7F);
  uint8_t mm=bcd2dec(Wire.read());
  uint8_t hh=bcd2dec(Wire.read() & 0x3F);
  if(softEpoch==0) return hh*3600UL + mm*60UL + ss;
  uint32_t days = softEpoch / 86400UL;
  return days*86400UL + hh*3600UL + mm*60UL + ss;
}
uint32_t nowEpoch(){
  if(hasRTC){ uint32_t r = rtcGet(); if(r) return r; }
  if(softEpoch) return softEpoch + (millis() - softMillis)/1000UL;
  return 0;
}
void syncTime(uint32_t epoch){ softEpoch = epoch; softMillis = millis(); if(hasRTC) rtcSet(epoch); }

void showSplash(){
  u8x8.clear();
  // Judul pakai font yang sama dengan jam: tiap font u8x8 ~3.5 KB flash, jadi
  // font tambahan khusus splash tidak sepadan. 8x13B_1x2 = 16x26 px, cukup besar.
  u8x8.setFont(u8x8_font_8x13B_1x2_f);
  drawCentered(0, "santra.");
  // baris 3: tagline kecil (6x10 px)
  u8x8.setFont(u8x8_font_5x7_f);
  drawCentered(u8x8.getRows() - 1, "pantau & kontrol", burnShift);
}
void showLines() {
  // Anti burn-in: u8x8 tidak bisa geser 2 px bebas (posisi terkunci grid font),
  // jadi digeser PER KOLOM karakter — 0/1 kolom = 0/6 px. Efeknya sama:
  // piksel tidak diam di titik yang sama terus.
  burnShift ^= 1;
  u8x8.clear();
  u8x8.setFont(u8x8_font_5x7_f);
  // 5x7_f: 8 baris x 21 kolom per layar, jadi 4 baris muat lega.
  for (uint8_t i = 0; i < 4; i++) {
    if (lines[i].length()) u8x8.drawString(burnShift, i, lines[i].c_str());
  }
}
void showClock(const char *t) {
  burnShift ^= 1;
  u8x8.clear();
  // 8x13B_1x2: sel 16x26 px, 5 char "10:30" = 80 px → 2 kolom sisa, bagi 2.
  u8x8.setFont(u8x8_font_8x13B_1x2_f);
  drawCentered(0, t, burnShift);
}
void showClockFromEpoch(uint32_t epochUTC){
  uint32_t wib = epochUTC + 7*3600UL;
  uint32_t days = wib / 86400UL;
  uint32_t sod = wib % 86400UL;
  uint8_t hh24 = sod / 3600;
  uint8_t mm = (sod % 3600) / 60;
  bool isPM = hh24 >= 12;
  uint8_t hh12 = hh24 % 12; if(hh12==0) hh12=12;
  char tbuf[6]; snprintf(tbuf,sizeof(tbuf),"%02u:%02u",hh12,mm);
  const char *ap = isPM ? "PM" : "AM";
  // civil date from days (Howard Hinnant)
  long z = (long)days + 719468;
  long era = (z >= 0 ? z : z - 146096) / 146097;
  unsigned doe = (unsigned)(z - era * 146097);
  unsigned yoe = (doe - doe/1460 + doe/36524 - doe/146096) / 365;
  int y = (int)(yoe + era * 400);
  unsigned doy = doe - (365*yoe + yoe/4 - yoe/100);
  unsigned mp = (5*doy + 2)/153;
  unsigned d = doy - (153*mp+2)/5 + 1;
  unsigned mo = mp + (mp < 10 ? 3 : -9);
  y += (mo <= 2);
  int wday = (days + 4) % 7; // 0=Min
  const char *wname[7]={"Ming","Sen","Sel","Rab","Kam","Jum","Sab"};
  const char *mname[12]={"Jan","Feb","Mar","Apr","Mei","Jun","Jul","Agu","Sep","Okt","Nov","Des"};
  char dbuf[24]; snprintf(dbuf,sizeof(dbuf),"%s %02u %s %d",wname[wday],(unsigned)d,mname[mo-1],y);
  burnShift ^= 1;
  u8x8.clear();
  // Baris 0-1: jam besar 8x13B_1x2 (sel 16x26 px). "10:30" = 5 char = 80 px.
  // AM/PM muat di 3 kolom sisa (48 px) pada font 6x10, jadi tidak perlu baris
  // sendiri — tapi u8x8 menulis per sel, bukan per piksel, jadi AM/PM ditaruh
  // di kolom 18 ke atas... tidak ada kolom 18. Solusi: jam 4 char (10:30 → 5
  // char tak muat bersama AM/PM). Karena itu jam dan AM/PM dipisah baris.
  u8x8.setFont(u8x8_font_8x13B_1x2_f);
  // Jam + AM/PM di baris yang sama: 8 kolom total, "10:30" 5 kolom, sisa 3
  // kolom untuk AM/PM pada font kecil butuh 2-3 kolom — jadi AM/PM ditaruh
  // menempel kanan jam tanpa mengganti baris.
  u8x8.drawString(1 + burnShift, 0, tbuf);
  u8x8.setFont(u8x8_font_5x7_f);
  u8x8.drawString(7, 0, ap);
  // Baris 6: tanggal (6x10 px per sel)
  drawCentered(u8x8.getRows() - 1, dbuf, burnShift);
}

void setup() {
  // WDT dulu dimatikan sebelum apa pun: kalau MCU baru direset WDT, flag MCUSR
  // masih set dan watchdog masih hidup → boot berikutnya reset lagi (loop).
  MCUSR = 0;
  wdt_disable();
  Serial.begin(9600);
  pinMode(7, OUTPUT); digitalWrite(7, HIGH); // relay CH1 active LOW OFF
  pinMode(8, OUTPUT); digitalWrite(8, HIGH); // relay CH2 active LOW OFF
  pinMode(13, OUTPUT);
  Wire.begin();
  // Batas waktu I2C: bus nyangkut / OLED lepas bikin endTransmission & u8x8
  // nunggu selamanya = bengong total (raw sunyi, DTR & power-cycle tak mempan).
  // 1200ms WAJIB < WDTO_2S: kalau timeout lebih panjang dari watchdog,
  // WDT reset MCU di tengah transfer I2C → slave (OLED/RTC) tetap menahan
  // SDA LOW → boot berikutnya nyangkut lagi = loop reset. Wire harus pulih
  // dan melepas bus DULU, baru WDT jadi lapis terakhir.
  Wire.setWireTimeout(1200, true);
  hasRTC = rtcProbe();
  u8x8.begin();
  u8x8.setPowerSave(0);
  u8x8.setFlipMode(0);
  showSplash();
  delay(1800);
  showLines();
  // Lapis terakhir: hang apa pun >2s (I2C/OLED/parse) = MCU reset sendiri,
  // UNO balik ke splash lalu terima push relay lagi. Tanpa ini bengong abadi
  // sampai tombol fisik dicabut.
  wdt_enable(WDTO_2S);
}

void loop() {
  wdt_reset(); // makan watchdog tiap iterasi; hang di bawah = reset 2s
  if (clockRestoreAt && (long)(millis() - clockRestoreAt) >= 0) { clockRestoreAt = 0; if(!clockAlwaysMode) showLines(); }
  if(clockAlwaysMode){
    uint32_t ep = nowEpoch();
    if(ep){
      uint32_t minute = ep / 60UL;
      if(minute != lastMinute){ lastMinute = minute; showClockFromEpoch(ep); }
    }
  }
  if (Serial.available()) {
    String s = Serial.readStringUntil('\n');
    s.trim();
    if (s.length() == 0) return;
    JsonDocument doc;
    if (deserializeJson(doc, s)) return;
    const char *cmd = doc["cmd"];
    if (!cmd) return;
    if (strcmp(cmd, "oled") == 0) {
      if(clockAlwaysMode) return;
      JsonArray arr = doc["lines"];
      if (!arr.isNull()) {
        uint8_t i = 0;
        for (JsonVariant v : arr) { if (i >= 4) break; lines[i++] = String((const char *)v.as<const char *>()); }
        showLines();
      }
    } else if (strcmp(cmd, "clock") == 0) {
      const char *t = doc["text"] | "88:88";
      int dur = doc["dur"] | 10; if (dur <= 0) dur = 10; if (dur > 90) dur = 90;
      if(!doc["epoch"].isNull()){ uint32_t e = doc["epoch"].as<uint32_t>(); if(e>1700000000UL) syncTime(e); }
      showClock(t);
      clockRestoreAt = millis() + (unsigned long)dur * 1000UL;
    } else if (strcmp(cmd, "clockAlways") == 0) {
      int on = doc["on"] | 0;
      uint32_t e = 0; if(!doc["epoch"].isNull()) e = doc["epoch"].as<uint32_t>();
      if(e>1700000000UL) syncTime(e);
      clockAlwaysMode = on;
      if(on){ lastMinute = 0xFFFFFFFF; uint32_t ep = nowEpoch(); if(ep) showClockFromEpoch(ep); else { const char *t = doc["text"] | "00:00"; showClock(t);} clockRestoreAt = 0; }
      else showLines();
    } else if (strcmp(cmd, "sync") == 0) {
      uint32_t e = doc["epoch"] | 0; if(e>1700000000UL) syncTime(e);
      if(clockAlwaysMode){ uint32_t ep = nowEpoch(); if(ep){ lastMinute = ep/60UL; showClockFromEpoch(ep);} }
    } else if (strcmp(cmd, "relay") == 0) {
      const char *id = doc["id"]; int on = doc["on"];
      if (id){
        if (strcmp(id, "lamp") == 0){ lampOn = on; digitalWrite(7, on ? LOW : HIGH); digitalWrite(13, on ? HIGH : LOW); }
        if (strcmp(id, "fan") == 0){ fanOn = on; digitalWrite(8, on ? LOW : HIGH); }
        // ack: bukti relay fisik sudah digerakkan (STB tunggu ini, tanpa ack = gagal)
        Serial.print(F("{\"ack\":\"")); Serial.print(id); Serial.print(F("\",\"on\":")); Serial.print(on); Serial.println(F("}"));
      }
    } else if (strcmp(cmd, "ping") == 0) {
      // Heartbeat: STB kirim tiap 5 dtk. Balasan pong membuktikan loop UNO masih
      // hidup — STB memakainya untuk mendeteksi UNO yang reset diam-diam (fd
      // serial masih valid, jadi tanpa ping relay tak pernah di-push ulang).
      Serial.print(F("{\"ack\":\"pong\",\"on\":1,\"lamp\":"));
      Serial.print(lampOn ? 1 : 0);
      Serial.print(F(",\"fan\":"));
      Serial.print(fanOn ? 1 : 0);
      Serial.println(F("}"));
    } else if (strcmp(cmd, "reset") == 0) {
      // ponytail: jump ke 0 = soft reset (tak sebersih DTR/power-cycle); cukup untuk hang loop, DTR fallback di STB.
      asm volatile ("jmp 0");
    }
  }
}
