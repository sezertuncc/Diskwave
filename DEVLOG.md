# Diskwave — Devlog

## Durum (son commit: `main` @ v1.2.2)

Repo şu an **private**. Tüm release'ler silindi.

---

## Tamamlanan Fix'ler

### v1.2.0
- `blockFile` write amplification: artık tüm dosya buffer'da tutuluyor, `Close()`'da tek `Put` yapılıyor
- `blockFile.Close()` → `meta.SetSize()` çağırıyor (Finder boyut hatası düzeltildi)
- `blocks.PutAll()` ve `blocks.Rename()` eklendi
- `tcp/quic handleRename`: `blockSvc.Delete` → `blockSvc.Rename` (rename'de veri kaybı düzeltildi)
- `WebDAVMounter`: `waitForMountPath` 10s polling kaldırıldı, Finder'da IP yerine "Diskwave" göster

### v1.2.1
- `dirFile.Readdir(count<=0)`: `io.EOF` yerine tüm entry'ler + `nil` error
  → `fts_read: Input/output error` hatası düzeltildi (Terminal, Cursor, VS Code çalışıyor)

### v1.2.2
- `blockFile.Write`: `make(end)+copy` → `append()` (O(n²) → O(n))
- `OpenFile` `O_TRUNC` flag: eski dosya yüklenmeden buffer oluşturuluyor, eski boyut capacity hint olarak kullanılıyor

---

## Açık Problem — YARININ İŞİ 🔴

### Çok dosyalı kopyalama çok yavaş (792 öğe → 4 saat)

**Ekran görüntüsünde görülen:** 792 öğe, 6.8MB toplam, tahmini 4 saat.

**Kök neden:** Her dosya için WebDAV ayrı HTTP request zinciri açıyor:
```
PROPFIND /klasor        → 1 round-trip
PUT /klasor/dosya1      → 1 round-trip (TCP handshake dahil)
PUT /klasor/dosya2      → 1 round-trip
...
```
792 dosya × ~2 round-trip = ~1584 seri HTTP isteği. Her biri sunucuya ayrı TCP bağlantısı veya en az ayrı istek. `http.ListenAndServe` default olarak `Keep-Alive` destekliyor ama macOS `mount_webdav` client'ı bunu her zaman kullanmıyor.

**Araştırılacak / denenecek yaklaşımlar:**
1. WebDAV sunucusuna `Connection: keep-alive` + `Keep-Alive` header'ı zorla
2. Go `http.Server`'a `ReadTimeout`, `WriteTimeout`, `IdleTimeout` optimize et — şu an default (unlimited)
3. `golang.org/x/net/webdav` handler'ın PROPFIND yanıtında `DAV: 1, 2` header eksik mi kontrol et
4. macOS WebDAV client ayarları: `nconnect` mount parametresi dene
5. Sunucuya HTTP/2 ekle — multiplexing, tek bağlantıda paralel istekler

**Hızlı deneme:**
```go
srv := &http.Server{
    Addr:        addr,
    Handler:     h,
    IdleTimeout: 120 * time.Second,  // keep-alive bağlantıları uzun tut
}
```

---

## Mimari Notlar

```
Mac (Swift)
  QUICClient (adı öyle ama TCP:7879) ──► tcp/handler
  WebDAV mount (HTTP:7881)           ──► webdav/handler
                                              ├─► metadata.Service (PostgreSQL + Redis 5s cache)
                                              └─► blocks.Service (MinIO/Local)
Mgmt API (HTTP:7880, localhost)      ──► mgmt/api  ← diskwave CLI

QUIC:7878 açık ama client bağlanmıyor (ileriye ertelenmiş)
```

## Bilinen Kalan Sorunlar

- TCP `authorized()` wrapper: `clientID == ""` olsa bile handler çalışıyor (guard pattern gerekli, kritik değil)
- QUIC handler ile TCP handler kod duplikasyonu — tek handler interface'i altında toplanabilir
- `blocks.Service` hâlâ tüm dosyayı RAM'e çekiyor (streaming yok) — büyük dosyalar için bellek baskısı
- WebDAV `RemoveAll` recursive ama seri — büyük dizin silme yavaş