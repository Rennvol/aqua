// santra UNO firmware — standby terima JSON dari STB via Serial 9600.
//
// Protokol (1 baris JSON + \n, 9600 baud):
//   STB -> UNO : {"cmd":"oled","lines":["a","b","c","d"]}  tampil 4 baris
//   STB -> UNO : {"cmd":"clock","text":"12:34","dur":10}   jam besar, balik dashboard otomatis
//   STB -> UNO : {"cmd":"relay","id":"lamp","on":1}        lamp -> LED pin13 (demo, relay fisik nyusul)
//   UNO -> STB : (diam, belum ada sensor; nanti {"temp":..,"voltage":..} tiap 2 detik)
//
// Upload: arduino-cli upload -p /dev/ttyACM0 --fqbn arduino:avr:uno .
// Libs: U8g2, ArduinoJson (v7 syntax JsonDocument)

#include <U8g2lib.h>
#include <ArduinoJson.h>

U8G2_SSD1306_128X64_NONAME_1_HW_I2C u8g2(U8G2_R0, U8X8_PIN_NONE);

String lines[4] = {"santra OK", "A4=SDA A5=SCL", "27 28 29 30", "L:OFF F:OFF"};
bool lampOn = false;
bool fanOn = false;
unsigned long clockRestoreAt = 0; // millis kapan jam selesai -> balik dashboard

void showLines() {
  u8g2.firstPage();
  do {
    u8g2.setFont(u8g2_font_ncenB08_tr);
    for (uint8_t i = 0; i < 4; i++) {
      u8g2.drawStr(0, 12 + i * 14, lines[i].c_str());
    }
  } while (u8g2.nextPage());
}

// showClock jam besar full-layar, teks tengah (HH:MM dari STB, UNO tak punya RTC).
void showClock(const char *t) {
  u8g2.firstPage();
  do {
    u8g2.setFont(u8g2_font_logisoso32_tr);
    uint8_t w = u8g2.getStrWidth(t);
    uint8_t x = w < 128 ? (128 - w) / 2 : 0;
    u8g2.drawStr(x, 44, t);
  } while (u8g2.nextPage());
}

void setup() {
  Serial.begin(9600);
  pinMode(13, OUTPUT); // demo: relay lamp -> LED L onboard
  u8g2.begin();
  showLines();
}

void loop() {
  if (clockRestoreAt && (long)(millis() - clockRestoreAt) >= 0) { clockRestoreAt = 0; showLines(); }
  if (Serial.available()) {
    String s = Serial.readStringUntil('\n');
    s.trim();
    if (s.length() == 0) return;
    JsonDocument doc;
    if (deserializeJson(doc, s)) return; // abaikan baris rusak
    const char *cmd = doc["cmd"];
    if (!cmd) return;
    if (strcmp(cmd, "oled") == 0) {
      JsonArray arr = doc["lines"];
      if (!arr.isNull()) {
        uint8_t i = 0;
        for (JsonVariant v : arr) {
          if (i >= 4) break;
          lines[i++] = String((const char *)v.as<const char *>());
        }
        showLines();
      }
    } else if (strcmp(cmd, "clock") == 0) {
      const char *t = doc["text"] | "88:88";
      int dur = doc["dur"] | 10;
      if (dur <= 0) dur = 10;
      if (dur > 90) dur = 90;
      showClock(t);
      clockRestoreAt = millis() + (unsigned long)dur * 1000UL;
    } else if (strcmp(cmd, "relay") == 0) {
      const char *id = doc["id"];
      int on = doc["on"];
      if (id) {
        if (strcmp(id, "lamp") == 0) { lampOn = on; digitalWrite(13, on ? HIGH : LOW); }
        if (strcmp(id, "fan") == 0) { fanOn = on; }
      }
    }
  }
}
