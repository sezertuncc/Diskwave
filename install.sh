#!/usr/bin/env bash
# Diskwave — one-command server install / upgrade
# Usage: curl -fsSL https://raw.githubusercontent.com/sezertuncc/Diskwave/main/install.sh | sudo bash

set -euo pipefail

trap 'echo "INSTALL FAILED at line $LINENO — command: $BASH_COMMAND" >&2' ERR

INSTALL_DIR="/opt/diskwave"
BIN="/usr/local/bin/diskwave"
REPO="https://github.com/sezertuncc/Diskwave"
RAW="https://raw.githubusercontent.com/sezertuncc/Diskwave/main"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; RESET='\033[0m'

info()    { echo -e "${BLUE}▸${RESET} $*"; }
success() { echo -e "${GREEN}✓${RESET} $*"; }
warn()    { echo -e "${YELLOW}⚠${RESET} $*"; }
fatal()   { echo -e "${RED}✗${RESET} $*" >&2; exit 1; }

banner() {
cat << 'EOF'

  ██████╗ ██╗███████╗██╗  ██╗██╗    ██╗ █████╗ ██╗   ██╗███████╗
  ██╔══██╗██║██╔════╝██║ ██╔╝██║    ██║██╔══██╗██║   ██║██╔════╝
  ██║  ██║██║███████╗█████╔╝ ██║ █╗ ██║███████║██║   ██║█████╗
  ██║  ██║██║╚════██║██╔═██╗ ██║███╗██║██╔══██║╚██╗ ██╔╝██╔══╝
  ██████╔╝██║███████║██║  ██╗╚███╔███╔╝██║  ██║ ╚████╔╝ ███████╗
  ╚═════╝ ╚═╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝ ╚═╝  ╚═╝  ╚═══╝  ╚══════╝

  Remote Disk Server — Setup
EOF
echo
}

# ── Dependencies ───────────────────────────────────────────────────────────────
check_deps() {
    info "Checking requirements..."
    command -v docker  >/dev/null 2>&1 || fatal "Docker not found. Install from https://docs.docker.com/engine/install"
    docker compose version >/dev/null 2>&1 || fatal "Docker Compose v2 not found."
    command -v curl    >/dev/null 2>&1 || fatal "curl not found."
    command -v git     >/dev/null 2>&1 || fatal "git not found. Run: apt install git"
    success "All requirements met"
}

# ── Install Go (if needed) ────────────────────────────────────────────────────
ensure_go() {
    if command -v go >/dev/null 2>&1; then
        return
    fi
    info "Installing Go..."
    GOARCH=$(uname -m)
    case "$GOARCH" in
        x86_64)  GOARCH="amd64" ;;
        aarch64|arm64) GOARCH="arm64" ;;
        *) fatal "Unsupported architecture: $GOARCH" ;;
    esac
    GO_VER="1.23.4"
    curl -fsSL "https://go.dev/dl/go${GO_VER}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz
    rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tar.gz
    export PATH=$PATH:/usr/local/go/bin
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
    success "Go ${GO_VER} installed"
}

# ── Build server binary ───────────────────────────────────────────────────────
build_server() {
    info "Building Diskwave server (latest)..."
    export PATH=$PATH:/usr/local/go/bin

    TMP=$(mktemp -d)
    git clone --depth=1 "$REPO" "$TMP/diskwave" >/dev/null 2>&1
    cd "$TMP/diskwave/server"
    go build -ldflags="-s -w" -o /usr/local/bin/diskwave-server ./cmd/server/
    go build -ldflags="-s -w" -o "$BIN" ./cmd/diskwave/
    cd /
    rm -rf "$TMP"
    success "Server binary ready: /usr/local/bin/diskwave-server"
    success "Management CLI ready: $BIN"
}

# ── Download compose file ──────────────────────────────────────────────────────
download_compose() {
    info "Setting up infrastructure (postgres, redis, minio)..."
    mkdir -p "$INSTALL_DIR"
    curl -fsSL "${RAW}/server/docker-compose.yml" -o "${INSTALL_DIR}/docker-compose.yml"
    success "docker-compose.yml downloaded"
}

# ── Ask (or generate) SMB password ────────────────────────────────────────────
prompt_smb_password() {
    # Reuse existing password on upgrades so Samba shares don't break
    if [[ -f "${INSTALL_DIR}/.env" ]]; then
        local existing
        existing=$(grep -E '^DISKWAVE_SMB_PASSWORD=' "${INSTALL_DIR}/.env" | cut -d'=' -f2- || true)
        if [[ -n "$existing" ]]; then
            SMB_PASSWORD="$existing"
            info "Keeping existing SMB password"
            return
        fi
    fi

    if [[ -t 0 ]]; then
        # Interactive terminal — ask the user
        echo
        echo -e "${BOLD}Set a password for the Samba share${RESET}"
        echo -e "${BLUE}  This password is used by the Mac app to mount the disk over SMB.${RESET}"
        echo -e "${BLUE}  Min 8 characters. Retrieve later with: diskwave smb-password${RESET}"
        echo
        while true; do
            read -rsp "  Password: " SMB_PASSWORD; echo
            if [[ ${#SMB_PASSWORD} -ge 8 ]]; then
                read -rsp "  Confirm : " SMB_CONFIRM; echo
                if [[ "$SMB_PASSWORD" == "$SMB_CONFIRM" ]]; then
                    break
                else
                    warn "Passwords do not match. Try again."
                fi
            else
                warn "Password must be at least 8 characters. Try again."
            fi
        done
        GENERATED_PASSWORD=0
    else
        # Non-interactive (piped curl | bash) — generate a cryptographically random password.
        # Avoid tr|head pipelines — they trigger SIGPIPE under set -o pipefail.
        if command -v openssl >/dev/null 2>&1; then
            SMB_PASSWORD=$(openssl rand -hex 14)   # 28 hex chars, no pipes needed
        else
            SMB_PASSWORD=$(dd if=/dev/urandom bs=14 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n')
        fi
        GENERATED_PASSWORD=1
    fi
}

# ── Write server env file ─────────────────────────────────────────────────────
write_env() {
    mkdir -p /opt/diskwave/data
    chmod 777 /opt/diskwave/data
    cat > "${INSTALL_DIR}/.env" << ENV
POSTGRES_URL=postgres://diskwave:diskwave@127.0.0.1:5432/diskwave?sslmode=disable
REDIS_ADDR=127.0.0.1:6379
STORAGE_TYPE=local
LOCAL_STORAGE_DIR=/opt/diskwave/data
QUIC_PORT=7878
TCP_PORT=7879
MGMT_PORT=7880
DISKWAVE_SMB_PASSWORD=${SMB_PASSWORD}
ENV
    chmod 600 "${INSTALL_DIR}/.env"
    success "Config written to ${INSTALL_DIR}/.env"
}

# ── Start infrastructure ───────────────────────────────────────────────────────
start_infra() {
    info "Starting infrastructure services..."
    cd "$INSTALL_DIR"
    docker compose up -d
    info "Waiting for postgres and redis to be healthy..."
    local attempts=0
    until docker compose ps | grep -c "healthy" | grep -qE "^[2-9]"; do
        attempts=$((attempts+1))
        [ $attempts -ge 60 ] && { warn "Services took too long. Check: docker compose logs"; break; }
        sleep 2
    done
    success "Infrastructure ready"
}

# ── Systemd service for server binary ─────────────────────────────────────────
install_systemd() {
    if ! command -v systemctl >/dev/null 2>&1; then return; fi

    cat > /etc/systemd/system/diskwave.service << EOF
[Unit]
Description=Diskwave Server
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
EnvironmentFile=${INSTALL_DIR}/.env
ExecStartPre=/usr/bin/docker compose -f ${INSTALL_DIR}/docker-compose.yml up -d
ExecStart=/usr/local/bin/diskwave-server
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable diskwave >/dev/null 2>&1
    systemctl start diskwave
    success "Diskwave service started (auto-start on boot enabled)"

    # Open required ports
    # Note: 445 (SMB) and 7880 (mgmt) are intentionally NOT opened —
    #   SMB is tunneled through the TLS connection on 7879 (never direct internet exposure)
    #   mgmt API binds to 127.0.0.1 only (localhost, no external access needed)
    if command -v ufw >/dev/null 2>&1; then
        ufw allow 7879/tcp >/dev/null 2>&1 && success "Port 7879 (TCP+TLS) opened"
        ufw delete allow 7880/tcp >/dev/null 2>&1 || true   # mgmt → localhost only
        ufw delete allow 445/tcp  >/dev/null 2>&1 || true   # SMB  → tunneled, not direct
    elif command -v firewall-cmd >/dev/null 2>&1; then
        firewall-cmd --permanent --add-port=7879/tcp >/dev/null 2>&1
        firewall-cmd --permanent --remove-port=7880/tcp >/dev/null 2>&1 || true
        firewall-cmd --permanent --remove-port=445/tcp  >/dev/null 2>&1 || true
        firewall-cmd --reload >/dev/null 2>&1
        success "Port 7879 opened (firewalld); 7880+445 restricted to localhost"
    else
        warn "Firewall not detected — make sure port 7879/tcp is open; do NOT open 445 or 7880"
    fi
}

# ── Show pair code ─────────────────────────────────────────────────────────────
show_pair_code() {
    info "Waiting for server to be ready..."
    local attempts=0
    until curl -sfk https://127.0.0.1:7880/status >/dev/null 2>&1; do
        attempts=$((attempts+1))
        [ $attempts -ge 30 ] && { warn "Server not ready yet. Try: diskwave pair-code"; return; }
        sleep 1
    done

    CODE=$(curl -sfk https://127.0.0.1:7880/pair-code 2>/dev/null | grep -o '"code":"[^"]*"' | cut -d'"' -f4 || echo "run 'diskwave pair-code'")
    echo
    echo -e "${BOLD}┌──────────────────────────────────────┐${RESET}"
    echo -e "${BOLD}│  Pairing Code: ${GREEN}${CODE}${RESET}${BOLD}                  │${RESET}"
    echo -e "${BOLD}│  Enter this code in the Mac app      │${RESET}"
    echo -e "${BOLD}└──────────────────────────────────────┘${RESET}"
    echo
}

# ── Uninstall ─────────────────────────────────────────────────────────────────
uninstall() {
    warn "Removing Diskwave..."
    systemctl stop diskwave 2>/dev/null || true
    systemctl disable diskwave 2>/dev/null || true
    rm -f /etc/systemd/system/diskwave.service
    systemctl daemon-reload 2>/dev/null || true
    cd "$INSTALL_DIR" 2>/dev/null && docker compose down -v || true
    rm -rf "$INSTALL_DIR"
    rm -f "$BIN" /usr/local/bin/diskwave-server
    success "Diskwave removed"
}

# ── Main ──────────────────────────────────────────────────────────────────────
main() {
    [[ "$(uname -s)" == "Linux" ]] && [[ $EUID -ne 0 ]] && fatal "Run as root: sudo bash install.sh"

    banner

    case "${1:-install}" in
        uninstall|remove) uninstall ;;
        install|*)
            check_deps
            ensure_go

            # Smart install/upgrade: already installed → stop service first, then rebuild
            if systemctl is-active --quiet diskwave 2>/dev/null; then
                info "Existing installation detected — upgrading to latest..."
                systemctl stop diskwave 2>/dev/null || true
            fi

            prompt_smb_password
            download_compose
            write_env
            start_infra
            build_server
            install_systemd
            show_pair_code

            # If password was auto-generated (non-interactive), show it prominently
            if [[ "${GENERATED_PASSWORD:-0}" == "1" ]]; then
                echo
                echo -e "${YELLOW}${BOLD}┌─────────────────────────────────────────────┐${RESET}"
                echo -e "${YELLOW}${BOLD}│  SAVE YOUR SMB PASSWORD                     │${RESET}"
                echo -e "${YELLOW}${BOLD}│                                             │${RESET}"
                printf  "${YELLOW}${BOLD}│  ${GREEN}%-43s${RESET}${YELLOW}${BOLD}│${RESET}\n" "${SMB_PASSWORD}"
                echo -e "${YELLOW}${BOLD}│                                             │${RESET}"
                echo -e "${YELLOW}${BOLD}│  Retrieve later: diskwave smb-password      │${RESET}"
                echo -e "${YELLOW}${BOLD}└─────────────────────────────────────────────┘${RESET}"
                echo
            fi

            echo -e "${BOLD}Setup complete!${RESET}"
            echo
            echo -e "  Management   : ${BLUE}diskwave${RESET}"
            echo -e "  Status       : ${BLUE}diskwave status${RESET}"
            echo -e "  Pairing code : ${BLUE}diskwave pair-code${RESET}"
            echo -e "  Clients      : ${BLUE}diskwave clients${RESET}"
            echo -e "  Uninstall    : ${BLUE}sudo diskwave uninstall${RESET}"
            echo
            echo -e "${YELLOW}⚠${RESET}  Open firewall port if needed:"
            echo -e "     ${BLUE}ufw allow 7879/tcp   # TCP+TLS (pairing + RPC + SMB tunnel)${RESET}"
            echo -e "     ${YELLOW}Do NOT open 445 or 7880 — SMB runs through the tunnel, mgmt is localhost-only${RESET}"
            echo
            ;;
    esac
}

main "$@"