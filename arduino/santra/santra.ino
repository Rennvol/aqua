// santra UNO firmware — standby terima JSON dari STB via Serial 9600.
// Protokol (1 baris JSON + \n, 9600 baud):
//   STB -> UNO : {"cmd":"oled","lines":["a","b","c","d"]}  tampil 4 baris
//   STB -> UNO : {"cmd":"clock","text":"12:34","dur":10}   jam besar 10 dtk
//   STB -> UNO : {"cmd":"clockAlways","on":1,"epoch":1710000000} jam terus self-tick
//   STB -> UNO : {"cmd":"sync","epoch":1710000000}         sync epoch UTC
//   STB -> UNO : {"cmd":"relay","id":"lamp","on":1}
// Splash boot = "santra." (bukan random).
// Anti burn-in: micro-shift 2px tiap refresh + jam digit selalu beda.
// Libs: U8g2, ArduinoJson (v7 JsonDocument)

#include <U8g2lib.h>
#include <ArduinoJson.h>
#include <Wire.h>

U8G2_SSD1306_128X64_NONAME_1_HW_I2C u8g2(U8G2_R0, U8X8_PIN_NONE);

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
  u8g2.firstPage();
  do {
    u8g2.setFont(u8g2_font_logisoso22_tr);
    const char *t="santra.";
    uint8_t w=u8g2.getStrWidth(t);
    u8g2.drawStr((128-w)/2, 32, t);
    u8g2.setFont(u8g2_font_6x10_tr);
    const char *s="pantau & kontrol";
    w=u8g2.getStrWidth(s);
    u8g2.drawStr((128-w)/2, 48, s);
  } while(u8g2.nextPage());
}
void showLines() {
  burnShift ^= 2; // toggle 0/2 px
  u8g2.firstPage();
  do {
    u8g2.setFont(u8g2_font_ncenB08_tr);
    for (uint8_t i = 0; i < 4; i++) {
      u8g2.drawStr(burnShift, 12 + i * 14, lines[i].c_str());
    }
  } while (u8g2.nextPage());
}
void showClock(const char *t) {
  burnShift ^= 2;
  u8g2.firstPage();
  do {
    u8g2.setFont(u8g2_font_logisoso32_tr);
    uint8_t w = u8g2.getStrWidth(t);
    uint8_t x = w < 128 ? (128 - w) / 2 + burnShift : burnShift;
    if(x>4) x-=1;
    u8g2.drawStr(x, 44, t);
  } while (u8g2.nextPage());
}
void showClockFromEpoch(uint32_t epochUTC){
  uint32_t wib = epochUTC + 7*3600UL;
  uint8_t hh = (wib % 86400UL)/3600;
  uint8_t mm = (wib % 3600UL)/60;
  char buf[6]; snprintf(buf,sizeof(buf),"%02u:%02u",hh,mm);
  showClock(buf);
}

void setup() {
  Serial.begin(9600);
  pinMode(13, OUTPUT);
  Wire.begin();
  hasRTC = rtcProbe();
  u8g2.begin();
  showSplash();
  delay(1800);
  showLines();
}

void loop() {
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
      if (id){ if (strcmp(id, "lamp") == 0){ lampOn = on; digitalWrite(13, on ? HIGH : LOW);} if (strcmp(id, "fan") == 0) fanOn = on; }
    }
  }
}
