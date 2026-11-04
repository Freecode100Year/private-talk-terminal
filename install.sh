#!/usr/bin/env bash
# Private-Talk Terminal — one-click installer / updater / uninstaller.
#
#   curl -fsSL https://raw.githubusercontent.com/Freecode100Year/private-talk-terminal/main/install.sh | sudo bash
#
# With options (note the "-s --"):
#   curl -fsSL .../install.sh | sudo bash -s -- --port 2222 --admin-key github:YourGitHubName
#
# Commands:  install (default) | upgrade | uninstall [--purge] | status | help
# Run "bash install.sh help" for every option.

set -Eeuo pipefail

REPO="Freecode100Year/private-talk-terminal"
BIN=/usr/local/bin/ptterm
CTL=/usr/local/sbin/ptterm-ctl
RAW_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"
UNIT=/etc/systemd/system/ptterm.service
DROPIN_DIR=/etc/systemd/system/ptterm.service.d
ENV_FILE=/etc/ptterm.env
CONF_DIR=/etc/ptterm
ADMIN_FILE=$CONF_DIR/admins.pub
STATE_DIR=/var/lib/ptterm
GO_VERSION="${GO_VERSION:-1.26.9}"
TORRC=/etc/tor/torrc
ONION_DIR=/var/lib/tor/ptterm
ONION_LOCAL=127.0.0.1:2224

CMD=install
PORT=""
VERSION="${PTT_VERSION:-latest}"
FROM_SOURCE=0
FIREWALL=1
TOR=0
ALT_PORT=""
PERSIST_BANS=""
PURGE=0
ROOMS=""
DOMAIN=""
ADMIN_KEYS=()
DOWNLOAD_BASE="${PTT_DOWNLOAD_BASE:-}"

# ---------------------------------------------------------------- output ----
if [ -t 1 ]; then
	C_B=$'\e[1m' C_G=$'\e[32m' C_Y=$'\e[33m' C_R=$'\e[31m' C_C=$'\e[36m' C_0=$'\e[0m'
else
	C_B="" C_G="" C_Y="" C_R="" C_C="" C_0=""
fi
info() { printf '%s==>%s %s\n' "$C_C" "$C_0" "$*"; }
ok() { printf '%s ✓%s %s\n' "$C_G" "$C_0" "$*"; }
warn() { printf '%s !%s %s\n' "$C_Y" "$C_0" "$*" >&2; }
die() {
	printf '%s ✗ %s%s\n' "$C_R" "$*" "$C_0" >&2
	exit 1
}

TMP=""
cleanup() { if [ -n "$TMP" ]; then rm -rf "$TMP"; fi; }
trap cleanup EXIT

usage() {
	cat <<EOF
${C_B}Private-Talk Terminal installer${C_0}

Usage: install.sh [command] [options]

Commands
  install            install or reconfigure (default)
  upgrade            install the newest release, keep configuration
  uninstall          remove service and binary (keeps config + host key)
  status             show service state and online counts
  help               this text

Options
  --port N           SSH port for the chat (default 2222, or the current one)
  --admin-key KEY    grant admin rights; KEY is "ssh-ed25519 AAAA...",
                     a path to a .pub file, or github:USERNAME. Repeatable.
  --rooms LIST       public rooms, e.g. "general:Lobby,chinese:中文,tech:技术"
  --domain NAME      domain to show in the final connect command
  --version vX.Y.Z   install a specific release (default: latest)
  --alt-port N       also listen on port N (e.g. 443, for users behind strict
                     firewalls); --alt-port off removes it
  --tor              also publish the chat as a Tor onion service, so users can
                     connect without the server ever seeing their IP address
  --persist-bans     keep bans across restarts (stores a hash key on disk; see README)
  --no-persist-bans  turn that off again (default: bans live in memory only)
  --from-source      build from source instead of downloading a release
  --no-firewall      don't touch ufw/firewalld/iptables
  --purge            with uninstall: also delete config, host key and admins

Environment
  PTT_DOWNLOAD_BASE  mirror serving ptterm-linux-<arch> and SHA256SUMS
  GOPROXY            Go module proxy for --from-source (e.g. https://goproxy.cn)
  GO_VERSION         Go toolchain for --from-source (default ${GO_VERSION})
EOF
}

# ------------------------------------------------------------------ args ----
parse_args() {
	if [ $# -gt 0 ]; then
		case "$1" in
		install | upgrade | uninstall | status | help)
			CMD=$1
			shift
			;;
		esac
	fi
	while [ $# -gt 0 ]; do
		case "$1" in
		--port) PORT=${2:?--port needs a value}; shift 2 ;;
		--port=*) PORT=${1#*=}; shift ;;
		--admin-key) ADMIN_KEYS+=("${2:?--admin-key needs a value}"); shift 2 ;;
		--admin-key=*) ADMIN_KEYS+=("${1#*=}"); shift ;;
		--rooms) ROOMS=${2:?--rooms needs a value}; shift 2 ;;
		--rooms=*) ROOMS=${1#*=}; shift ;;
		--domain) DOMAIN=${2:?--domain needs a value}; shift 2 ;;
		--domain=*) DOMAIN=${1#*=}; shift ;;
		--version) VERSION=${2:?--version needs a value}; shift 2 ;;
		--version=*) VERSION=${1#*=}; shift ;;
		--from-source) FROM_SOURCE=1; shift ;;
		--tor) TOR=1; shift ;;
		--alt-port) ALT_PORT=${2:?--alt-port needs a value}; shift 2 ;;
		--alt-port=*) ALT_PORT=${1#*=}; shift ;;
		--persist-bans) PERSIST_BANS=on; shift ;;
		--no-persist-bans) PERSIST_BANS=off; shift ;;
		--no-firewall) FIREWALL=0; shift ;;
		--purge) PURGE=1; shift ;;
		--print-unit) CMD=print-unit; shift ;;
		-h | --help) CMD=help; shift ;;
		*) die "unknown option: $1 (see: install.sh help)" ;;
		esac
	done
	if [ -n "$PORT" ]; then
		[[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "invalid port: $PORT"
	fi
	if [ -n "$ALT_PORT" ] && [ "$ALT_PORT" != off ]; then
		[[ "$ALT_PORT" =~ ^[0-9]+$ ]] && [ "$ALT_PORT" -ge 1 ] && [ "$ALT_PORT" -le 65535 ] || die "invalid --alt-port: $ALT_PORT"
	fi
}

# ---------------------------------------------------------------- checks ----
need_root() { [ "$(id -u)" -eq 0 ] || die "please run as root (sudo bash install.sh ...)"; }

need_systemd() {
	[ -d /run/systemd/system ] || die "systemd is required (this system doesn't appear to run it)"
	command -v systemctl >/dev/null || die "systemctl not found"
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	armv7l | armv7*) ARCH=armv7 ;;
	*) die "unsupported CPU architecture: $(uname -m) (supported: x86_64, aarch64, armv7)" ;;
	esac
	[ "$(uname -s)" = Linux ] || die "Linux only"
}

fetch() { # fetch URL DEST
	if command -v curl >/dev/null; then
		curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
	elif command -v wget >/dev/null; then
		wget -q --tries=3 --timeout=15 -O "$2" "$1"
	else
		die "need curl or wget"
	fi
}

fetch_stdout() {
	if command -v curl >/dev/null; then curl -fsSL --connect-timeout 10 "$1"; else wget -qO- --timeout=10 "$1"; fi
}

port_open() { # is something accepting connections on local TCP port $1?
	if command -v ss >/dev/null; then
		ss -Hltn "sport = :$1" 2>/dev/null | grep -q .
	else
		(exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
	fi
}

port_owner() { # prints the process listening on TCP port $1 ("unknown" if it can't tell)
	port_open "$1" || return 0
	local name=""
	if command -v ss >/dev/null; then
		name=$(ss -Hltnp "sport = :$1" 2>/dev/null | sed -n 's/.*users:(("\([^"]*\)".*/\1/p' | head -1)
	fi
	if [ -z "$name" ] && systemctl is-active --quiet ptterm >/dev/null 2>&1 && [ "$(current_port)" = "$1" ]; then
		name=ptterm
	fi
	echo "${name:-unknown}"
}

current_alt_port() {
	if [ -f "$ENV_FILE" ]; then
		sed -n 's/^PTT_ALT_ADDR=.*:\([0-9]\{1,5\}\)$/\1/p' "$ENV_FILE" | tail -1
	fi
}

current_port() {
	if [ -f "$ENV_FILE" ]; then
		sed -n 's/^PTT_ADDR=.*:\([0-9]\{1,5\}\)$/\1/p' "$ENV_FILE" | tail -1
	fi
}

# --------------------------------------------------------------- binary -----
download_release() {
	local base
	if [ -n "$DOWNLOAD_BASE" ]; then
		base=${DOWNLOAD_BASE%/}
	elif [ "$VERSION" = latest ]; then
		base="https://github.com/$REPO/releases/latest/download"
	else
		base="https://github.com/$REPO/releases/download/$VERSION"
	fi
	info "Downloading ptterm-linux-$ARCH from $base"
	fetch "$base/ptterm-linux-$ARCH" "$TMP/ptterm" || return 1
	fetch "$base/SHA256SUMS" "$TMP/SHA256SUMS" || return 1
	(cd "$TMP" && grep " ptterm-linux-$ARCH\$" SHA256SUMS | sed "s/ptterm-linux-$ARCH/ptterm/" | sha256sum -c --quiet -) ||
		die "checksum mismatch for ptterm-linux-$ARCH — refusing to install"
	ok "Checksum verified"
}

build_from_source() {
	local goarch=$ARCH goarm="" ref url
	[ "$ARCH" = armv7 ] && goarch=arm goarm=7
	local go="$TMP/go/bin/go"
	if command -v go >/dev/null && GOTOOLCHAIN=local go version 2>/dev/null | grep -Eq 'go1\.(2[6-9]|[3-9][0-9])'; then
		go=$(command -v go)
		info "Using installed $(GOTOOLCHAIN=local go version | awk '{print $3}')"
	else
		info "Downloading Go $GO_VERSION (temporary, removed afterwards)"
		local tgz="go$GO_VERSION.linux-$goarch.tar.gz" fetched=0
		for site in https://go.dev/dl https://golang.google.cn/dl https://dl.google.com/go; do
			if fetch "$site/$tgz" "$TMP/$tgz" 2>/dev/null; then
				fetched=1
				break
			fi
		done
		[ $fetched = 1 ] || die "could not download Go toolchain"
		if fetch "https://dl.google.com/go/$tgz.sha256" "$TMP/go.sha256" 2>/dev/null; then
			echo "$(cat "$TMP/go.sha256")  $TMP/$tgz" | sha256sum -c --quiet - || die "Go toolchain checksum mismatch"
		else
			warn "could not fetch Go checksum; continuing (TLS-verified download)"
		fi
		tar -C "$TMP" -xzf "$TMP/$tgz"
	fi
	if [ "$VERSION" = latest ]; then ref=heads/main; else ref=tags/$VERSION; fi
	url="https://github.com/$REPO/archive/refs/$ref.tar.gz"
	info "Fetching source ($ref)"
	fetch "$url" "$TMP/src.tgz" || die "could not download source from $url"
	mkdir -p "$TMP/src"
	tar -C "$TMP/src" --strip-components=1 -xzf "$TMP/src.tgz"
	local ver=$VERSION
	[ "$ver" = latest ] && ver="main-$(date -u +%Y%m%d)"
	info "Building (this takes a minute on small VPSes)"
	(cd "$TMP/src" && HOME="$TMP" GOPATH="$TMP/gopath" GOCACHE="$TMP/gocache" GOFLAGS=-mod=readonly \
		GOTOOLCHAIN=local CGO_ENABLED=0 GOARCH=$goarch GOARM=$goarm \
		"$go" build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$TMP/ptterm" .) ||
		die "build failed"
	ok "Built from source"
}

get_binary() {
	TMP=$(mktemp -d)
	if [ $FROM_SOURCE = 1 ]; then
		build_from_source
	elif ! download_release; then
		warn "release download failed — building from source instead"
		build_from_source
	fi
	chmod 0755 "$TMP/ptterm"
	"$TMP/ptterm" -version >/dev/null || die "downloaded binary doesn't run on this system"
}

# ----------------------------------------------------------------- unit -----
print_unit() {
	cat <<'EOF'
[Unit]
Description=Private-Talk Terminal (anonymous SSH chat)
Documentation=https://github.com/Freecode100Year/private-talk-terminal
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# DynamicUser creates a throwaway unprivileged user; StateDirectory keeps the
# SSH host key in /var/lib/ptterm across restarts and upgrades.
DynamicUser=yes
StateDirectory=ptterm
WorkingDirectory=/var/lib/ptterm
Environment=PTT_ADDR=:2222
EnvironmentFile=-/etc/ptterm.env
ExecStart=/usr/local/bin/ptterm -addr ${PTT_ADDR} -host-key /var/lib/ptterm/host_ed25519 $PTT_ARGS
Restart=always
RestartSec=2

# Hardening: the chat process can't touch the rest of the system.
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallErrorNumber=EPERM
UMask=0077
LimitNOFILE=65536
# Never write process memory (which holds chat messages) to disk.
LimitCORE=0
Environment=GOTRACEBACK=single
TasksMax=8192

[Install]
WantedBy=multi-user.target
EOF
}

# ------------------------------------------------------------- config -------
set_env() { # set_env KEY VALUE — replace or append a line in $ENV_FILE
	local k=$1 v=$2
	if grep -q "^$k=" "$ENV_FILE" 2>/dev/null; then
		local esc
		esc=$(printf '%s' "$v" | sed 's/[&|\\]/\\&/g')
		sed -i "s|^$k=.*|$k=$esc|" "$ENV_FILE"
	else
		printf '%s=%s\n' "$k" "$v" >>"$ENV_FILE"
	fi
}

mem_limit() { # GOMEMLIMIT ≈ 60% of RAM, at least 128 MiB
	local kb mib
	kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 1048576)
	mib=$((kb * 6 / 10 / 1024))
	[ "$mib" -lt 128 ] && mib=128
	echo "${mib}MiB"
}

write_env() {
	if [ ! -f "$ENV_FILE" ]; then
		cat >"$ENV_FILE" <<EOF
# Private-Talk Terminal configuration. After editing:  systemctl restart ptterm
# Full flag list:  ptterm -h

PTT_ADDR=:${PORT:-2222}

# Counts-only health endpoint, used by "install.sh status". Localhost only.
PTT_HEALTH_ADDR=127.0.0.1:2223

# Soft memory target for the Go runtime (set from this machine's RAM).
GOMEMLIMIT=$(mem_limit)

# Public rooms, first one is the lobby. Empty = built-in defaults.
#PTT_ROOMS=general:大厅 Lobby,chinese:中文频道,english:English,technology:技术,anonymous:随便聊

# Interface language for clients that send no LANG (zh or en).
#PTT_LANG=zh

# Keep bans across restarts (install.sh --persist-bans). Off by default: it
# stores the address-hash key on disk next to the ban list.
#PTT_BAN_FILE=/var/lib/ptterm/bans.json

# Extra welcome lines shown on connect (a text file).
#PTT_MOTD=/etc/ptterm/motd.txt

# Any other flags.
PTT_ARGS=-history 50 -history-ttl 1h -max-per-ip 3 -idle-timeout 1h
EOF
		chmod 0644 "$ENV_FILE"
		ok "Created $ENV_FILE"
	elif [ -n "$PORT" ]; then
		set_env PTT_ADDR ":$PORT"
	fi
	[ -n "$ROOMS" ] && set_env PTT_ROOMS "$ROOMS"
	case "$ALT_PORT" in
	"") ;;
	off) sed -i '/^PTT_ALT_ADDR=/d' "$ENV_FILE" ;;
	*) set_env PTT_ALT_ADDR ":$ALT_PORT" ;;
	esac
	case "$PERSIST_BANS" in
	on) set_env PTT_BAN_FILE "$STATE_DIR/bans.json" ;;
	off)
		sed -i '/^PTT_BAN_FILE=/d' "$ENV_FILE"
		rm -f /var/lib/private/ptterm/ban.key /var/lib/private/ptterm/bans.json "$STATE_DIR/ban.key" "$STATE_DIR/bans.json"
		;;
	esac
	PORT=$(current_port)
	PORT=${PORT:-2222}
}

add_admin_keys() {
	[ ${#ADMIN_KEYS[@]} -eq 0 ] && return 0
	mkdir -p "$CONF_DIR"
	touch "$ADMIN_FILE"
	local spec keys n=0
	for spec in "${ADMIN_KEYS[@]}"; do
		case "$spec" in
		github:*) keys=$(fetch_stdout "https://github.com/${spec#github:}.keys") || die "could not fetch keys for $spec" ;;
		ssh-* | ecdsa-* | sk-*) keys=$spec ;;
		*) [ -f "$spec" ] || die "admin key file not found: $spec"; keys=$(cat "$spec") ;;
		esac
		while IFS= read -r line; do
			[ -z "$line" ] && continue
			case "$line" in ssh-* | ecdsa-* | sk-*) ;; *) continue ;; esac
			local body
			body=$(awk '{print $1" "$2}' <<<"$line")
			if ! grep -qF "$body" "$ADMIN_FILE"; then
				echo "$line" >>"$ADMIN_FILE"
				n=$((n + 1))
			fi
		done <<<"$keys"
	done
	chmod 0644 "$ADMIN_FILE"
	set_env PTT_ADMIN_KEYS "$ADMIN_FILE"
	ok "Admin keys: $n added ($(grep -c . "$ADMIN_FILE") total in $ADMIN_FILE)"
}

write_unit() {
	print_unit >"$UNIT"
	rm -f "$DROPIN_DIR/low-port.conf"
	local alt
	alt=$(current_alt_port)
	if [ "$PORT" -lt 1024 ] || { [ -n "$alt" ] && [ "$alt" -lt 1024 ]; }; then
		mkdir -p "$DROPIN_DIR"
		cat >"$DROPIN_DIR/low-port.conf" <<'EOF'
# Added by install.sh: allow binding a privileged port (< 1024).
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
EOF
	fi
	rmdir "$DROPIN_DIR" 2>/dev/null || true
	systemctl daemon-reload
}

# ------------------------------------------------------------- firewall -----
open_firewall() {
	[ $FIREWALL = 1 ] || return 0
	local p=$1
	if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
		ufw allow "$p/tcp" comment "private-talk-terminal" >/dev/null && ok "ufw: allowed $p/tcp"
		return
	fi
	if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd -q --permanent --add-port="$p/tcp" && firewall-cmd -q --reload && ok "firewalld: allowed $p/tcp"
		return
	fi
	if command -v iptables >/dev/null && iptables -S INPUT 2>/dev/null | grep -Eq -- '-P INPUT DROP|-j (REJECT|DROP)'; then
		# e.g. Oracle Cloud images ship a REJECT-all rule at the end of INPUT
		local changed=0 t
		for t in iptables ip6tables; do
			command -v $t >/dev/null || continue
			if ! $t -C INPUT -p tcp --dport "$p" -j ACCEPT 2>/dev/null; then
				$t -I INPUT 1 -p tcp --dport "$p" -m comment --comment private-talk-terminal -j ACCEPT 2>/dev/null && changed=1
			fi
		done
		if [ $changed = 1 ]; then
			if command -v netfilter-persistent >/dev/null; then
				netfilter-persistent save >/dev/null 2>&1 || true
			elif [ -d /etc/iptables ]; then
				iptables-save >/etc/iptables/rules.v4
				command -v ip6tables-save >/dev/null && ip6tables-save >/etc/iptables/rules.v6
			elif [ -d /etc/sysconfig ] && [ -f /etc/sysconfig/iptables ]; then
				iptables-save >/etc/sysconfig/iptables
			else
				warn "iptables rule added but not persisted; it will be lost on reboot"
			fi
			ok "iptables: allowed $p/tcp"
		fi
		return
	fi
	if command -v nft >/dev/null && nft list ruleset 2>/dev/null | grep -q "policy drop"; then
		warn "nftables has a drop policy; allow TCP $p yourself, e.g. in /etc/nftables.conf"
	fi
}

close_firewall() {
	local p=$1
	if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "$p/tcp"; then
		ufw delete allow "$p/tcp" >/dev/null 2>&1 || true
	fi
	if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd -q --permanent --remove-port="$p/tcp" 2>/dev/null && firewall-cmd -q --reload || true
	fi
	local t
	for t in iptables ip6tables; do
		command -v $t >/dev/null || continue
		while $t -D INPUT -p tcp --dport "$p" -m comment --comment private-talk-terminal -j ACCEPT 2>/dev/null; do :; done
	done
}

# ------------------------------------------------------------------ tor -----
pkg_install() {
	if command -v apt-get >/dev/null; then
		DEBIAN_FRONTEND=noninteractive apt-get install -y -q "$@" >/dev/null || {
			apt-get update -q >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -q "$@" >/dev/null
		}
	elif command -v dnf >/dev/null; then
		dnf install -y -q "$@" || { dnf install -y -q epel-release && dnf install -y -q "$@"; }
	elif command -v yum >/dev/null; then
		yum install -y -q epel-release >/dev/null 2>&1 || true
		yum install -y -q "$@"
	elif command -v zypper >/dev/null; then
		zypper -q install -y "$@"
	elif command -v pacman >/dev/null; then
		pacman -Sy --noconfirm "$@" >/dev/null
	else
		return 1
	fi
}

setup_tor() {
	if ! command -v tor >/dev/null; then
		info "Installing Tor"
		pkg_install tor || die "could not install the 'tor' package; install it yourself and rerun with --tor"
	fi
	[ -f "$TORRC" ] || touch "$TORRC"
	if ! grep -q "BEGIN private-talk-terminal" "$TORRC"; then
		printf '\n# BEGIN private-talk-terminal (managed by install.sh)\nHiddenServiceDir %s/\nHiddenServicePort 22 %s\n# END private-talk-terminal\n' \
			"$ONION_DIR" "$ONION_LOCAL" >>"$TORRC"
	fi
	set_env PTT_ONION_ADDR "$ONION_LOCAL"
	systemctl enable tor >/dev/null 2>&1 || true
	systemctl restart tor
	# Debian/Ubuntu run the daemon as tor@default behind the "tor" wrapper unit
	if systemctl cat tor@default.service >/dev/null 2>&1; then
		systemctl restart tor@default || true
	fi
	for _ in $(seq 1 60); do
		[ -s "$ONION_DIR/hostname" ] && break
		sleep 0.5
	done
	[ -s "$ONION_DIR/hostname" ] || die "Tor did not create the onion service; check: journalctl -u tor@default -u tor"
	ok "Onion service: $(cat "$ONION_DIR/hostname")"
}

remove_tor() {
	if [ -f "$TORRC" ] && grep -q "BEGIN private-talk-terminal" "$TORRC"; then
		sed -i '/^# BEGIN private-talk-terminal/,/^# END private-talk-terminal/d' "$TORRC"
		systemctl restart tor >/dev/null 2>&1 || true
	fi
	if [ $PURGE = 1 ]; then
		rm -rf "$ONION_DIR"
	fi
}

onion_name() {
	if [ -s "$ONION_DIR/hostname" ]; then
		cat "$ONION_DIR/hostname"
	fi
}

# --------------------------------------------------------------- report -----
public_ip() {
	local ip
	ip=$(fetch_stdout https://api.ipify.org 2>/dev/null || true)
	[[ "$ip" =~ ^[0-9.]+$ ]] || ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	echo "${ip:-YOUR_SERVER_IP}"
}

wait_listening() {
	for _ in $(seq 1 30); do
		if systemctl is-active --quiet ptterm && port_open "$1"; then
			sleep 1 # make sure it stays up (e.g. not crash-looping on bad flags)
			systemctl is-active --quiet ptterm && return 0
		fi
		sleep 0.5
	done
	return 1
}

summary() {
	local host=${DOMAIN:-$(public_ip)} portflag="" fp=""
	[ "$PORT" != 22 ] && portflag="-p $PORT "
	if command -v ssh-keygen >/dev/null && [ -f "$STATE_DIR/host_ed25519" ]; then
		fp=$(ssh-keygen -lf "$STATE_DIR/host_ed25519" 2>/dev/null | awk '{print $2}')
	fi
	cat <<EOF

${C_G}${C_B}Private-Talk Terminal is running.${C_0}  ($("$BIN" -version))

  Connect:        ${C_B}ssh ${portflag}guest@$host${C_0}
  Plain mode:     ssh ${portflag}plain@$host
EOF
	local alt
	alt=$(current_alt_port)
	if [ -n "$alt" ]; then
		echo "  Firewalled?    ssh -p $alt guest@$host   (alternate port)"
	fi
	local onion
	onion=$(onion_name)
	if [ -n "$onion" ]; then
		echo "  Via Tor:        ${C_B}torsocks ssh guest@$onion${C_0}"
		echo "                  (or: ssh -o ProxyCommand='nc -x 127.0.0.1:9050 %h %p' guest@$onion)"
	fi
	[ -n "$fp" ] && echo "  Host key:       $fp   (users see this on first connect)"
	cat <<EOF
  Config:         $ENV_FILE   (then: systemctl restart ptterm)
  Logs:           journalctl -u ptterm -f
  Manage:         sudo ptterm-ctl status | upgrade | uninstall

  Reminders
   • Cloud firewalls (Oracle VCN security list, AWS/GCP security groups, …)
     must also allow inbound TCP $PORT.
   • On Cloudflare, the DNS record must be ${C_B}DNS only${C_0} (grey cloud) — SSH can't go through the proxy.
   • Back up $STATE_DIR/host_ed25519; a new key makes clients warn about a changed host.
EOF
}

# Keep a copy of this script as "ptterm-ctl" so status/upgrade/uninstall work
# later even when it was run via "curl | bash".
install_ctl() {
	local src="${BASH_SOURCE[0]:-}"
	if [ -n "$src" ] && [ -f "$src" ] && grep -q "Private-Talk Terminal — one-click installer" "$src" 2>/dev/null; then
		[ "$(readlink -f "$src")" = "$CTL" ] || install -m 0755 "$src" "$CTL"
	elif fetch "$RAW_URL" "$TMP/ctl.sh" 2>/dev/null && grep -q "one-click installer" "$TMP/ctl.sh"; then
		install -m 0755 "$TMP/ctl.sh" "$CTL"
	else
		return 0
	fi
	ok "Management command: ptterm-ctl (status | upgrade | uninstall)"
}

# ------------------------------------------------------------- commands -----
do_install() {
	need_root
	need_systemd
	detect_arch
	local was_installed=0
	[ -x "$BIN" ] && systemctl list-unit-files ptterm.service >/dev/null 2>&1 && was_installed=1

	local want=${PORT:-$(current_port)}
	want=${want:-2222}
	local owner
	owner=$(port_owner "$want")
	if [ -n "$owner" ] && [ "$owner" != ptterm ]; then
		if [ "$want" = 22 ] && [[ "$owner" == sshd* ]]; then
			die "port 22 is used by your system sshd. Move sshd to another port first (and keep a session open!), then rerun with --port 22."
		fi
		die "port $want is already in use by '$owner'. Choose another with --port N."
	fi

	if [ -n "$ALT_PORT" ] && [ "$ALT_PORT" != off ]; then
		[ "$ALT_PORT" = "$want" ] && die "--alt-port must differ from the main port ($want)"
		owner=$(port_owner "$ALT_PORT")
		if [ -n "$owner" ] && [ "$owner" != ptterm ]; then
			die "port $ALT_PORT is already in use by '$owner' (a web server on 443?). Pick another --alt-port, or skip it."
		fi
	fi

	get_binary
	install -m 0755 "$TMP/ptterm" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
	ok "Installed $BIN ($("$BIN" -version))"

	mkdir -p "$CONF_DIR"
	local old_port old_alt
	old_port=$(current_port || true)
	old_alt=$(current_alt_port || true)
	write_env
	add_admin_keys
	write_unit
	install_ctl
	if [ $TOR = 1 ]; then setup_tor; fi
	open_firewall "$PORT"
	if [ -n "$old_port" ] && [ "$old_port" != "$PORT" ]; then
		close_firewall "$old_port"
	fi
	local alt
	alt=$(current_alt_port)
	[ -n "$alt" ] && open_firewall "$alt"
	if [ -n "$old_alt" ] && [ "$old_alt" != "$alt" ]; then
		close_firewall "$old_alt"
	fi

	systemctl enable ptterm >/dev/null 2>&1
	if [ $was_installed = 1 ]; then
		systemctl restart ptterm
	else
		systemctl start ptterm
	fi
	if ! wait_listening "$PORT"; then
		journalctl -u ptterm -n 20 --no-pager >&2 || true
		die "service did not start; see the log above"
	fi
	ok "Service running on port $PORT"
	summary
}

do_upgrade() {
	[ -x "$BIN" ] || die "not installed yet — run: install.sh install"
	VERSION=${VERSION:-latest}
	local before
	before=$("$BIN" -version 2>/dev/null || echo unknown)
	do_install
	info "Upgraded: $before → $("$BIN" -version)"
}

do_uninstall() {
	need_root
	local p
	p=$(current_port || true)
	systemctl disable --now ptterm >/dev/null 2>&1 || true
	rm -f "$UNIT"
	rm -rf "$DROPIN_DIR"
	systemctl daemon-reload 2>/dev/null || true
	rm -f "$BIN"
	[ -n "$p" ] && close_firewall "$p"
	local alt
	alt=$(current_alt_port || true)
	[ -n "$alt" ] && close_firewall "$alt"
	remove_tor
	[ "$(readlink -f "${BASH_SOURCE[0]:-}")" = "$CTL" ] || rm -f "$CTL"
	[ $PURGE = 1 ] && rm -f "$CTL"
	if [ $PURGE = 1 ]; then
		rm -rf "$ENV_FILE" "$CONF_DIR" "$STATE_DIR" /var/lib/private/ptterm
		ok "Removed service, binary, configuration and host key"
	else
		ok "Removed service and binary (kept $ENV_FILE, $CONF_DIR and the host key; use --purge to delete)"
	fi
}

do_status() {
	if ! [ -x "$BIN" ]; then
		echo "Private-Talk Terminal is not installed."
		exit 1
	fi
	echo "Version:  $("$BIN" -version)"
	echo "Service:  $(systemctl is-active ptterm 2>/dev/null || true) ($(systemctl is-enabled ptterm 2>/dev/null || true))"
	echo "Port:     $(current_port || echo 2222)$( [ -n "$(current_alt_port)" ] && echo " (also $(current_alt_port))")"
	local onion
	onion=$(onion_name)
	if [ -n "$onion" ]; then echo "Onion:    $onion (Tor)"; fi
	if command -v ssh-keygen >/dev/null && [ -f "$STATE_DIR/host_ed25519" ]; then
		echo "Host key: $(ssh-keygen -lf "$STATE_DIR/host_ed25519" 2>/dev/null | awk '{print $2}')  (publish this so users can verify it)"
	fi
	local h
	h=$(sed -n 's/^PTT_HEALTH_ADDR=//p' "$ENV_FILE" 2>/dev/null | tail -1)
	if [ -n "$h" ] && command -v curl >/dev/null; then
		echo "Health:   $(curl -fsS --max-time 3 "http://$h/healthz" 2>/dev/null || echo unreachable)"
	fi
	journalctl -u ptterm -n 5 --no-pager 2>/dev/null || true
}

main() {
	parse_args "$@"
	case "$CMD" in
	install) do_install ;;
	upgrade) do_upgrade ;;
	uninstall) do_uninstall ;;
	status) do_status ;;
	print-unit) print_unit ;;
	help) usage ;;
	esac
}

main "$@"
