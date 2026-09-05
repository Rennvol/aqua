// santra UNO firmware — standby terima JSON dari STB via Serial 9600.
//
// Protokol (1 baris JSON + \n, 9600 baud):
//   STB -> UNO : {"cmd":"oled","lines":["a","b","c","d"]}  tampil 4 baris
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

void showLines() {
  u8g2.firstPage();
  do {
    u8g2.setFont(u8g2_font_ncenB08_tr);
    for (uint8_t i = 0; i < 4; i++) {
      u8g2.drawStr(0, 12 + i * 14, lines[i].c_str());
    }
  } while (u8g2.nextPage());
}

void setup() {
  Serial.begin(9600);
  pinMode(13, OUTPUT); // demo: relay lamp -> LED L onboard
  u8g2.begin();
  showLines();
}

void loop() {
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
