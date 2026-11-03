#!/usr/bin/env bash
# Подготовка сервера; production entry point — deploy/app/bootstrap.sh.
# Идемпотентен: повторный запуск ничего не ломает и пропускает готовые шаги.
#
# Использование (от root):
#   bash bootstrap.sh --deploy-pubkey "ssh-ed25519 AAAA... github-actions" \
#                     --admin-pubkey  "ssh-ed25519 AAAA... admin" \
#                     [--registry-mirror https://mirror.gcr.io] [--harden-ssh]
set -Eeuo pipefail

TOTAL_STEPS=8
DEPLOY_USER="deploy"
APP_DIR="/opt/maxim"
SWAP_FILE="/swapfile"
SWAP_SIZE_GB=2
SSHD_DROPIN="/etc/ssh/sshd_config.d/10-maxim.conf"

DEPLOY_PUBKEY=""
ADMIN_PUBKEY=""
REGISTRY_MIRROR=""
HARDEN_SSH=0

log() {
  printf '[%s] [bootstrap] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

step() {
  log "[step $1/${TOTAL_STEPS}] $2"
}

die() {
  log "ERROR: $*"
  exit 1
}

trap 'log "ERROR: command failed at line ${LINENO}"' ERR

usage() {
  sed -n '2,9p' "$0"
  exit 2
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --deploy-pubkey) DEPLOY_PUBKEY="${2:-}"; shift 2 ;;
      --admin-pubkey) ADMIN_PUBKEY="${2:-}"; shift 2 ;;
      --registry-mirror) REGISTRY_MIRROR="${2:-}"; shift 2 ;;
      --harden-ssh) HARDEN_SSH=1; shift ;;
      --app-dir) APP_DIR="${2:?--app-dir requires a path}"; shift 2 ;;
      -h|--help) usage ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  [[ -n "$DEPLOY_PUBKEY" ]] || die "--deploy-pubkey is required"
  [[ -n "$ADMIN_PUBKEY" ]] || die "--admin-pubkey is required"
  [[ "$APP_DIR" =~ ^/opt/[a-zA-Z0-9_-]+$ ]] || die "--app-dir must be a direct directory under /opt"
  [[ "$(id -u)" -eq 0 ]] || die "must run as root"
  log "args: registry_mirror='${REGISTRY_MIRROR:-none}' harden_ssh=${HARDEN_SSH}"
}

# Дописывает ключ в authorized_keys, если его там ещё нет.
add_authorized_key() {
  local home="$1" owner="$2" key="$3"
  install -d -m 700 -o "$owner" -g "$owner" "$home/.ssh"
  touch "$home/.ssh/authorized_keys"
  if grep -qxF "$key" "$home/.ssh/authorized_keys"; then
    log "key already present for $owner (${key##* })"
  else
    printf '%s\n' "$key" >> "$home/.ssh/authorized_keys"
    log "key added for $owner (${key##* })"
  fi
  chown "$owner:$owner" "$home/.ssh/authorized_keys"
  chmod 600 "$home/.ssh/authorized_keys"
}

check_os() {
  step 1 "check OS"
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="$ID"
  OS_CODENAME="${VERSION_CODENAME:-}"
  OS_PRETTY="$PRETTY_NAME"
  case "${ID}:${VERSION_ID}" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:12) log "OS supported: ${PRETTY_NAME}" ;;
    *) die "unsupported OS: ${PRETTY_NAME} (need Ubuntu 22.04/24.04/26.04 or Debian 12)" ;;
  esac
}

install_base_packages() {
  step 2 "apt upgrade and base packages"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -q
  apt-get -y -q -o Dpkg::Options::=--force-confold upgrade
  apt-get -y -q install ca-certificates curl gnupg ufw unattended-upgrades dnsutils
  # Только обновления безопасности (настройка пакета по умолчанию), без автоперезагрузки.
  cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
  log "done"
}

configure_time_and_swap() {
  step 3 "timezone and swap"
  timedatectl set-timezone UTC
  local mem_mb
  mem_mb=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
  if swapon --show --noheadings | grep -q .; then
    log "swap already configured, skip (RAM ${mem_mb} MB)"
  elif (( mem_mb >= 2048 )); then
    log "RAM ${mem_mb} MB >= 2048, swap not needed, skip"
  else
    log "RAM ${mem_mb} MB < 2048, creating ${SWAP_SIZE_GB}G swap at ${SWAP_FILE}"
    fallocate -l "${SWAP_SIZE_GB}G" "$SWAP_FILE"
    chmod 600 "$SWAP_FILE"
    mkswap "$SWAP_FILE" >/dev/null
    swapon "$SWAP_FILE"
    grep -qF "$SWAP_FILE" /etc/fstab || echo "$SWAP_FILE none swap sw 0 0" >> /etc/fstab
    log "done"
  fi
}

install_docker() {
  step 4 "Docker Engine and compose plugin"
  if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
    log "docker already installed: $(docker --version)"
  else
    if curl -fsS -o /dev/null "https://download.docker.com/linux/${OS_ID}/dists/${OS_CODENAME}/Release"; then
      log "using official Docker apt repository (${OS_ID} ${OS_CODENAME})"
      install -m 0755 -d /etc/apt/keyrings
      curl -fsSL "https://download.docker.com/linux/${OS_ID}/gpg" -o /etc/apt/keyrings/docker.asc
      chmod a+r /etc/apt/keyrings/docker.asc
      echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${OS_ID} ${OS_CODENAME} stable" \
        > /etc/apt/sources.list.d/docker.list
      apt-get update -q
      apt-get -y -q install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    else
      # У Docker ещё нет репозитория под свежий релиз ОС — ставим пакеты дистрибутива.
      log "Docker repo has no '${OS_CODENAME}', falling back to distro packages (docker.io, docker-compose-v2)"
      apt-get -y -q install docker.io docker-compose-v2 docker-buildx
    fi
    log "installed: $(docker --version)"
  fi

  local daemon_json
  if [[ -n "$REGISTRY_MIRROR" ]]; then
    daemon_json=$(printf '{\n  "log-driver": "json-file",\n  "log-opts": {"max-size": "10m", "max-file": "3"},\n  "registry-mirrors": ["%s"]\n}\n' "$REGISTRY_MIRROR")
  else
    daemon_json=$(printf '{\n  "log-driver": "json-file",\n  "log-opts": {"max-size": "10m", "max-file": "3"}\n}\n')
  fi
  if [[ -f /etc/docker/daemon.json ]]; then
    log "preserving existing daemon.json; configure any registry mirror manually"
  else
    printf '%s\n' "$daemon_json" > /etc/docker/daemon.json
    systemctl restart docker
    log "daemon.json written, docker restarted"
  fi
  systemctl enable --now docker >/dev/null
  log "done"
}

configure_users() {
  step 5 "users and SSH keys"
  if id "$DEPLOY_USER" >/dev/null 2>&1; then
    log "user ${DEPLOY_USER} exists"
  else
    useradd --create-home --shell /bin/bash "$DEPLOY_USER"
    log "user ${DEPLOY_USER} created"
  fi
  usermod -aG docker "$DEPLOY_USER"
  add_authorized_key "/home/${DEPLOY_USER}" "$DEPLOY_USER" "$DEPLOY_PUBKEY"
  add_authorized_key /root root "$ADMIN_PUBKEY"
  log "done"
}

prepare_app_dir() {
  step 6 "application directory ${APP_DIR}"
  install -d -m 750 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$APP_DIR"
  log "done"
}

configure_firewall() {
  step 7 "firewall (ufw)"
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw allow 22/tcp >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  ufw allow 443/udp >/dev/null
  ufw --force enable >/dev/null
  log "done"
}

harden_ssh() {
  step 8 "SSH hardening"
  if (( HARDEN_SSH == 0 )); then
    log "skip (run again with --harden-ssh after verifying key login)"
    return
  fi
  cat > "$SSHD_DROPIN" <<'EOF'
# Managed by deploy/bootstrap.sh
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password

# Хостер кладёт 99-qemu.conf с "Match User root / PasswordAuthentication yes".
# В sshd побеждает первое значение, поэтому перекрываем его своим Match раньше.
Match User root
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    PermitRootLogin prohibit-password
EOF
  # Ubuntu cloud-образы кладут PasswordAuthentication yes в 50-cloud-init.conf;
  # наш файл с префиксом 10- читается раньше и тоже побеждает.
  sshd -t
  systemctl reload ssh 2>/dev/null || systemctl reload sshd
  local root_auth
  root_auth=$(sshd -T -C user=root,host=localhost,addr=127.0.0.1 | awk '/^passwordauthentication / {print $2}')
  [[ "$root_auth" == "no" ]] || die "password auth for root still enabled (passwordauthentication=${root_auth}); check /etc/ssh/sshd_config.d/"
  log "done (root password auth: ${root_auth})"
}

summary() {
  log "summary:"
  log "  os:      ${OS_PRETTY}"
  log "  docker:  $(docker --version)"
  log "  compose: $(docker compose version --short)"
  log "  memory:  $(free -m | awk '/Mem:/ {print $2" MB RAM"} /Swap:/ {print $2" MB swap"}' | paste -sd ', ')"
  log "  disk /:  $(df -h / | awk 'NR==2 {print $4" free of "$2}')"
  log "  ufw:     $(ufw status | head -1)"
  sshd -T -C user=root,host=localhost,addr=127.0.0.1 2>/dev/null | grep -E '^(passwordauthentication|permitrootlogin) ' | while read -r line; do log "  sshd:    root $line"; done
}

main() {
  parse_args "$@"
  log "start"
  check_os
  install_base_packages
  configure_time_and_swap
  install_docker
  configure_users
  prepare_app_dir
  configure_firewall
  harden_ssh
  summary
  log "finished"
}

main "$@"
