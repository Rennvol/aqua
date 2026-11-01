// Uji parser tanpa UNO: gcc -o /tmp/pt parser_test.c && /tmp/pt
// Payload di bawah adalah yang BENAR-BENAR dikirim serial.go (json.Marshal),
// bukan karangan — termasuk kasus tepi: nilai kosong, escape, angka negatif,
// kunci yang mirip (on vs epoch), dan perintah cacat.
#include <stdio.h>
#include <assert.h>
#include "jsonmini.h"

static void t_str_ok(void){
  char buf[16];
  assert(jmStr("{\"cmd\":\"relay\"}", "\"cmd\"", buf, sizeof buf));
  assert(strcmp(buf, "relay") == 0);

  // spasi setelah titik dua (json.Marshal tidak kirim spasi, tapi host lain bisa)
  assert(jmStr("{\"cmd\": \"oled\"}", "\"cmd\"", buf, sizeof buf));
  assert(strcmp(buf, "oled") == 0);

  // nilai kosong
  assert(jmStr("{\"id\":\"\"}", "\"id\"", buf, sizeof buf));
  assert(strcmp(buf, "") == 0);
}

static void t_str_escape(void){
  char buf[24];
  // garis miring & kutip di-escape oleh json.Marshal
  assert(jmStr("{\"t\":\"a\\\"b\"}", "\"t\"", buf, sizeof buf));
  assert(strcmp(buf, "a\"b") == 0);
  assert(jmStr("{\"t\":\"a\\\\b\"}", "\"t\"", buf, sizeof buf));
  assert(strcmp(buf, "a\\b") == 0);
}

static void t_str_missing(void){
  char buf[16];
  assert(!jmStr("{\"cmd\":\"relay\"}", "\"id\"", buf, sizeof buf));
  assert(!jmStr("{\"cmd\":1}", "\"cmd\"", buf, sizeof buf)); // angka, bukan string
  assert(!jmStr(NULL, "\"cmd\"", buf, sizeof buf));
}

static void t_int(void){
  assert(jmInt("{\"on\":1}", "\"on\"", -1) == 1);
  assert(jmInt("{\"on\":0}", "\"on\"", -1) == 0);
  assert(jmInt("{\"dur\":90}", "\"dur\"", 10) == 90);
  assert(jmInt("{\"epoch\":1710000000}", "\"epoch\"", 0) == 1710000000L);
  // kunci tak ada -> default
  assert(jmInt("{\"on\":1}", "\"epoch\"", 77) == 77);
  // bukan angka -> default
  assert(jmInt("{\"on\":\"1\"}", "\"on\"", 42) == 42);
  // angka negatif (tak dipakai host, tapi jangan sampai salah baca)
  assert(jmInt("{\"on\":-5}", "\"on\"", 0) == -5);
}

static void t_int_key_clash(void){
  // "on" adalah substring "epoch"? tidak. Tapi "on" ada di dalam "clockAlways"
  // payload: {"cmd":"clockAlways","on":1,...} — pastikan "on" ketemu yang benar.
  const char *p = "{\"cmd\":\"clockAlways\",\"on\":1,\"epoch\":1710000000}";
  assert(jmInt(p, "\"on\"", -1) == 1);
  assert(jmInt(p, "\"epoch\"", 0) == 1710000000L);
}

static void t_array(void){
  char out[4][JM_STRMAX];
  uint8_t n = jmStrArray("{\"cmd\":\"oled\",\"lines\":[\"santra.\",\"pantau & kontrol\",\"menunggu STB...\",\"\"]}",
                         "\"lines\"", out, 4);
  assert(n == 4);
  assert(strcmp(out[0], "santra.") == 0);
  assert(strcmp(out[1], "pantau & kontrol") == 0);
  assert(strcmp(out[2], "menunggu STB...") == 0);
  assert(strcmp(out[3], "") == 0);

  // array 2 elemen, sisanya tak boleh tersentuh
  n = jmStrArray("{\"lines\":[\"a\",\"b\"]}", "\"lines\"", out, 4);
  assert(n == 2 && strcmp(out[0], "a") == 0 && strcmp(out[1], "b") == 0);

  // tanpa kunci
  assert(jmStrArray("{\"cmd\":\"sync\"}", "\"lines\"", out, 4) == 0);
}

static void t_truncation(void){
  // baris panjang dipotong: harus berhenti di buffer, tidak menulis lewat
  char buf[8];
  assert(jmStr("{\"t\":\"abcdefghijklmnop\"}", "\"t\"", buf, sizeof buf));
  assert(strcmp(buf, "abcdefg") == 0); // 7 char + NUL
}

static void t_garbage(void){
  char buf[16];
  // bukan JSON sama sekali: tidak boleh crash, semua jatuh ke default
  assert(!jmStr("sampah", "\"cmd\"", buf, sizeof buf));
  assert(jmInt("sampah", "\"on\"", 9) == 9);
  // JSON terpotong (STB mati di tengah kirim)
  assert(!jmStr("{\"cmd\":\"rel", "\"cmd\"", buf, sizeof buf));
  assert(jmInt("{\"on\":", "\"on\"", 5) == 5);
}

int main(void){
  t_str_ok(); t_str_escape(); t_str_missing(); t_int();
  t_int_key_clash(); t_array(); t_truncation(); t_garbage();
  puts("parser: 8 grup tes LOLOS");
  return 0;
}
