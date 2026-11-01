// Parser JSON datar minimal untuk wire format santra (6 perintah, tanpa
// bersarang). Pengganti ArduinoJson: hemat ~5,9 KB flash dari 32 KB.
//
// Batasan yang DITERIMA (bukan bug): tidak memvalidasi struktur JSON. Kurung
// atau escape rusak tidak ditolak — hanya nilai per kunci yang dicari. Aman
// karena semua pemanggil memberi default, jadi perintah cacat jatuh ke perilaku
// aman, bukan ke nilai sampah. Kalau host suatu hari mengirim JSON bersarang
// (objek di dalam objek), parser ini tidak cukup: pakai ArduinoJson lagi.
//
// ponytail: pencarian linear per kunci (strstr), O(n·k) untuk n panjang baris
// dan k jumlah kunci. Baris host < 200 byte, jadi tidak terukur. Ganti ke satu
// pass kalau suatu hari baris jadi KB-an.
//
// Sengaja murni C (tanpa String/Arduino) supaya bisa diuji di PC:
//   gcc -o /tmp/parser_test parser_test.c && /tmp/parser_test
#ifndef JSONMINI_H
#define JSONMINI_H

#include <stdint.h>
#include <stdbool.h>
#include <string.h>
#include <stdlib.h>

#define JM_STRMAX 40

// Cari posisi setelah kunci, mis. jmKey(s, "\"cmd\"") -> menunjuk ke ':'.
static const char* jmKey(const char *s, const char *needle){
  const char *p = strstr(s, needle);
  return p ? p + strlen(needle) : NULL;
}

static void jmSkipWs(const char **pp){
  while (**pp == ' ' || **pp == '\t') (*pp)++;
}

// Baca string: {"key":"isi"} -> buf. Escape \" dan \\ dilewati (satu karakter
// sesudah backslash diambil apa adanya). Return false kalau kunci tak ada, atau
// nilainya bukan string, ATAU string tidak ditutup kutip (baris terpotong).
//
// Penolakan baris terpotong itu WAJIB: tanpa itu, {"cmd":"relay","id":"lamp
// (STB mati di tengah kirim) terbaca id="lamp" sementara "on" hilang dan jatuh
// ke default 0 — lampu mati tanpa ada yang memerintahkan. ArduinoJson lama
// menolak baris seperti ini; perilaku itu dipertahankan.
static bool jmStr(const char *s, const char *needle, char *buf, uint8_t n){
  if (!s || !buf || n < 2) return false;
  const char *p = jmKey(s, needle);
  if (!p) return false;
  jmSkipWs(&p);
  if (*p != ':') return false;
  p++;
  jmSkipWs(&p);
  if (*p != '"') return false;
  p++;
  uint8_t i = 0;
  while (*p && *p != '"' && i < n - 1){
    if (*p == '\\' && p[1]) p++;   // lewati backslash, ambil char sesudahnya
    buf[i++] = *p++;
  }
  buf[i] = 0;
  if (*p != '"'){
    // Buffer penuh tapi nilainya masih berlanjut: buang sisanya sampai kutip
    // penutup. Kalau kutipnya ada, nilainya sah (cuma kepanjangan untuk buffer)
    // — potong, jangan tolak. Kalau tidak ada, barisnya memang terpotong.
    while (*p && *p != '"'){
      if (*p == '\\' && p[1]) p++;
      p++;
    }
    if (*p != '"') return false;
  }
  return true;
}

// Baca angka: {"key":123} -> 123, atau def kalau tak ada / bukan angka.
static long jmInt(const char *s, const char *needle, long def){
  if (!s) return def;
  const char *p = jmKey(s, needle);
  if (!p) return def;
  jmSkipWs(&p);
  if (*p != ':') return def;
  p++;
  jmSkipWs(&p);
  if (*p != '-' && (*p < '0' || *p > '9')) return def;
  return strtol(p, NULL, 10);
}

// Baca array string: {"lines":["a","b"]} -> out[0..]. Return jumlah terisi.
static uint8_t jmStrArray(const char *s, const char *needle, char out[][JM_STRMAX], uint8_t max){
  if (!s || !out || !max) return 0;
  const char *p = jmKey(s, needle);
  if (!p) return 0;
  while (*p && *p != '[') p++;
  if (*p != '[') return 0;
  p++;
  uint8_t i = 0;
  while (*p && *p != ']' && i < max){
    if (*p == '"'){
      p++;
      uint8_t j = 0;
      while (*p && *p != '"' && j < JM_STRMAX - 1){
        if (*p == '\\' && p[1]) p++;
        out[i][j++] = *p++;
      }
      out[i][j] = 0;
      i++;
      if (*p == '"') p++;
    } else p++;
  }
  return i;
}

#endif
