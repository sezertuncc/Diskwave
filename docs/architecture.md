# Teknik Mimari

## Genel yapı

```
Sunucu (VPS / Homelab)          Mac
┌──────────────────┐             ┌───────────────────────┐
│  diskwave server │  ◄── TCP ──►│  Diskwave.app         │
│                  │             │                       │
│  PostgreSQL      │             │  /Volumes/Diskwave ───│── Finder
│  Redis           │             │  (macFUSE)            │
│  MinIO / S3      │             └───────────────────────┘
└──────────────────┘
```

## Bileşenler

### Server (Go)

| Servis | Port | Görev |
|--------|------|-------|
| QUIC   | 7878 | Gelecekte native Swift transport |
| TCP    | 7879 | Mac client RPC bağlantısı |
| Mgmt   | 7880 | `diskwave` CLI için HTTP API (localhost only) |

- **auth** — 6 haneli rotating pairing kodu, Ed25519 imzalı JWT
- **metadata** — Dosya sistemi ağacı PostgreSQL'de, sık erişim Redis cache'de
- **blocks** — Dosya içeriği MinIO / S3-uyumlu depolama veya local disk
- **tcp** — Protobuf envelope framing, çoklu eşzamanlı stream

### Mac Client (Swift)

- `QUICClient` — TCP + protobuf RPC; pair/connect async, FUSE callback'leri için senkron
- `DiskMountManager` — macFUSE high-level API; getattr, readdir, read, write, truncate, mkdir, create, unlink, rename
- `AppState` — Bağlantı durumu, hız istatistikleri, mount yönetimi

### Protobuf protokolü

Tüm mesajlar `Envelope` içinde taşınır:

```
Envelope {
  request_id: uint32   // istek-yanıt eşleştirme
  type: MessageType    // 25 mesaj tipi
  payload: bytes       // serialized proto mesajı
}
```

Wire format: `4-byte big-endian length` + `serialized Envelope`

### Push notifications

Sunucu, `mkdir / rename / delete` gibi mutasyonlarda tüm bağlı client'lara `INVALIDATE` mesajı gönderir — cache anında temizlenir.

## Storage katmanı

```
files ──► blocks.Service ──► storage.Adapter
                                   ├── MinIOAdapter (production)
                                   └── LocalAdapter  (development)
```

Dosyalar MinIO'da path hash'i üzerinden saklanır. PostgreSQL inode ağacını tutar; Redis 5 saniyelik stat/readdir cache sağlar.

## Proje yapısı

```
server/
├── cmd/server/      Ana sunucu
├── cmd/diskwave/    TUI yönetim CLI (Bubble Tea)
└── internal/
    ├── auth/        Pairing + JWT
    ├── metadata/    Dosya sistemi (PostgreSQL + Redis)
    ├── blocks/      Dosya içeriği (MinIO / local)
    ├── tcp/         Transport handler
    ├── mgmt/        HTTP yönetim API
    └── storage/     Storage adapter interface

client/Sources/
├── App/             SwiftUI menu bar uygulaması
├── Network/         TCP + protobuf RPC client
├── FUSE/            macFUSE entegrasyonu
└── Proto/           Protobuf generated Swift
```