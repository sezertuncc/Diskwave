<p align="center">
  <a href="https://github.com/sezertuncc/Diskwave/releases/latest/download/DiskWave.dmg">
    <img src="assets/logo.png" width="420" alt="Diskwave" />
  </a>
</p>

<p align="center">
  Mount your remote server as a real disk on your Mac.<br/>
  Drag, drop, open files — just like a USB drive plugged in.
</p>

<p align="center">
  <a href="https://github.com/sezertuncc/Diskwave/releases/latest/download/DiskWave.dmg">
    <img src="https://img.shields.io/badge/-%E2%AC%87%EF%B8%8F%20%20Download%20Diskwave-1A9AE0?style=for-the-badge&logoColor=white" height="44" alt="Download Diskwave" />
  </a>
</p>

<p align="center">
  <sub>macOS 13 Ventura or later &nbsp;·&nbsp; Apple Silicon & Intel</sub>
</p>

---

## How it works

- Install Diskwave on your Mac
- Run one command on your server — a **6-digit pairing code** appears
- Enter the server IP and code in the app
- `/Volumes/Diskwave` appears in Finder — use it like any local disk

[Technical architecture →](docs/architecture.md)

---

## Server setup

Paste this into your server terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/sezertuncc/Diskwave/main/install.sh | sudo bash
```

The installer pulls Docker images, starts all services, and prints your pairing code.

### Server CLI

```bash
diskwave              # interactive dashboard
diskwave pair-code    # print current pairing code
diskwave clients      # list connected devices
diskwave unpair <id>  # revoke a device
```

---

## Requirements

| Mac    | macOS 13+  ·  macFUSE *(installed automatically on first launch)* |
|--------|-------------------------------------------------------------------|
| Server | Docker  ·  Docker Compose v2                                      |

---

<p align="center">
  <img src="https://img.shields.io/badge/platform-macOS%2013%2B-blue?style=flat-square" />
  <img src="https://img.shields.io/badge/server-Go-00ADD8?style=flat-square&logo=go" />
  <img src="https://img.shields.io/badge/license-MIT-green?style=flat-square" />
</p>

<p align="center"><sub>MIT License</sub></p>