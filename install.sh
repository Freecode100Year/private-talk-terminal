#!/usr/bin/env bash
# VIVANT — one-click installer / updater / uninstaller.
# VIVANT 一键安装 / 升级 / 卸载
#
#   curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh | sudo bash
#
# With options (note the "-s --"):
#   curl -fsSL .../install.sh | sudo bash -s -- --port 2222 --admin-key github:YourGitHubName
#
# Commands:  install (default) | upgrade | uninstall [--purge] | status | doctor | help
# Run "bash install.sh help" for every option.

set -Eeuo pipefail

REPO="Freecode100Year/vivant"
BIN=/usr/local/bin/vivant
CTL=/usr/local/sbin/vivant-ctl
RAW_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"
UNIT=/etc/systemd/system/vivant.service
DROPIN_DIR=/etc/systemd/system/vivant.service.d
ENV_FILE=/etc/vivant.env
CONF_DIR=/etc/vivant
ADMIN_FILE=$CONF_DIR/admins.pub
STATE_DIR=/var/lib/vivant
GO_VERSION="${GO_VERSION:-1.26.9}"
TORRC=/etc/tor/torrc
ONION_DIR=/var/lib/tor/vivant
ONION_LOCAL=127.0.0.1:2224

CMD=install
PORT=""
VERSION="${VIVANT_VERSION:-${PTT_VERSION:-latest}}"
FROM_SOURCE=0
FIREWALL=1
TOR=0
ALT_PORT=""
PERSIST_BANS=""
PURGE=0
ROOMS=""
DOMAIN=""
ADMIN_KEYS=()
DOWNLOAD_BASE="${VIVANT_DOWNLOAD_BASE:-${PTT_DOWNLOAD_BASE:-}}"

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
${C_B}VIVANT 安装器 · installer${C_0}

用法 Usage: install.sh [命令 command] [选项 options]

命令 Commands
  install            安装或修改配置（默认）· install or reconfigure (default)
  upgrade            升级到最新版，保留配置 · install the newest release, keep configuration
  uninstall          删除服务和程序，保留配置和主机密钥 · remove service and binary (keeps config + host key)
  status             运行状态和在线人数 · service state and online counts
  doctor             自检：服务、端口、防火墙、域名/Cloudflare、主机密钥、新版本，并给出修法
                     check service, ports, firewall, DNS/Cloudflare, host key, updates; say how to fix
  help               显示本帮助 · this text

选项 Options
  --port N           聊天室的 SSH 端口（默认 2222 或当前值）· SSH port for the chat (default 2222, or the current one)
  --admin-key KEY    授予管理员权限；KEY 可以是 "ssh-ed25519 AAAA..."、.pub 文件路径或 github:用户名，可重复
                     grant admin rights; KEY is "ssh-ed25519 AAAA...", a .pub file, or github:USERNAME. Repeatable.
  --rooms LIST       公共房间 public rooms, e.g. "general:大厅 Lobby,chinese:中文,tech:技术"
  --domain NAME      用户连接用的域名（显示在连接命令里，doctor 也用它检查解析）
                     domain users connect to (shown in the connect command, used by doctor DNS checks)
  --version vX.Y.Z   安装指定版本（默认最新）· install a specific release (default: latest)
  --alt-port N       同时监听端口 N（如 443，给防火墙严格的网络）；--alt-port off 关闭
                     also listen on port N (e.g. 443, for strict firewalls); --alt-port off removes it
  --tor              同时发布为 Tor 洋葱服务，服务器看不到用户 IP
                     also publish the chat as a Tor onion service, so the server never sees users' IPs
  --persist-bans     重启后保留封禁（会把哈希密钥存盘，见文档）· keep bans across restarts (stores a hash key on disk)
  --no-persist-bans  关闭上一项（默认封禁只在内存）· turn that off again (default: bans live in memory only)
  --from-source      从源码编译，不下载发布版 · build from source instead of downloading a release
  --no-firewall      不动 ufw/firewalld/iptables · don't touch ufw/firewalld/iptables
  --purge            配合 uninstall：连配置、主机密钥、管理员一起删除 · with uninstall: also delete config, host key and admins

环境变量 Environment
  VIVANT_DOWNLOAD_BASE  提供 vivant-linux-<arch> 和 SHA256SUMS 的镜像 · mirror serving vivant-linux-<arch> and SHA256SUMS
  GOPROXY               --from-source 用的 Go 模块代理 · Go module proxy for --from-source (e.g. https://goproxy.cn)
  GO_VERSION            --from-source 用的 Go 版本（默认 ${GO_VERSION}）· Go toolchain for --from-source

从 Private-Talk Terminal（ptterm）升级 · Upgrading from Private-Talk Terminal (ptterm):
  运行本脚本即可自动迁移：配置、主机密钥、封禁、洋葱地址都会保留。
  Just run this script: configuration, host key, bans and onion address are carried over.
EOF
}

# ------------------------------------------------------------------ args ----
parse_args() {
	if [ $# -gt 0 ]; then
		case "$1" in
		install | upgrade | uninstall | status | doctor | help)
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
		*) die "未知选项 unknown option: $1（见 see: install.sh help）" ;;
		esac
	done
	if [ -n "$PORT" ]; then
		[[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die "端口无效 invalid port: $PORT"
	fi
	if [ -n "$ALT_PORT" ] && [ "$ALT_PORT" != off ]; then
		[[ "$ALT_PORT" =~ ^[0-9]+$ ]] && [ "$ALT_PORT" -ge 1 ] && [ "$ALT_PORT" -le 65535 ] || die "--alt-port 无效 invalid: $ALT_PORT"
	fi
}

# ---------------------------------------------------------------- checks ----
need_root() { [ "$(id -u)" -eq 0 ] || die "请用 root 运行 · please run as root (sudo bash install.sh ...)"; }

need_systemd() {
	[ -d /run/systemd/system ] || die "需要 systemd，这台系统似乎没有运行它 · systemd is required (this system doesn't appear to run it)"
	command -v systemctl >/dev/null || die "找不到 systemctl · systemctl not found"
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	armv7l | armv7*) ARCH=armv7 ;;
	*) die "不支持的 CPU 架构 unsupported CPU architecture: $(uname -m)（支持 supported: x86_64, aarch64, armv7）" ;;
	esac
	[ "$(uname -s)" = Linux ] || die "只支持 Linux · Linux only"
}

fetch() { # fetch URL DEST
	if command -v curl >/dev/null; then
		curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
	elif command -v wget >/dev/null; then
		wget -q --tries=3 --timeout=15 -O "$2" "$1"
	else
		die "需要 curl 或 wget · need curl or wget"
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
	if [ -z "$name" ] && systemctl is-active --quiet vivant >/dev/null 2>&1 && [ "$(current_port)" = "$1" ]; then
		name=vivant
	fi
	echo "${name:-unknown}"
}

current_alt_port() {
	if [ -f "$ENV_FILE" ]; then
		sed -n 's/^VIVANT_ALT_ADDR=.*:\([0-9]\{1,5\}\)$/\1/p' "$ENV_FILE" | tail -1
	fi
}

current_port() {
	if [ -f "$ENV_FILE" ]; then
		sed -n 's/^VIVANT_ADDR=.*:\([0-9]\{1,5\}\)$/\1/p' "$ENV_FILE" | tail -1
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
	local asset=vivant-linux-$ARCH
	info "下载 Downloading $asset ← $base"
	if ! fetch "$base/$asset" "$TMP/vivant" 2>/dev/null; then
		asset=ptterm-linux-$ARCH # releases before the rename (--version v0.8.x)
		fetch "$base/$asset" "$TMP/vivant" || return 1
	fi
	fetch "$base/SHA256SUMS" "$TMP/SHA256SUMS" || return 1
	(cd "$TMP" && grep " $asset\$" SHA256SUMS | sed "s/$asset/vivant/" | sha256sum -c --quiet -) ||
		die "校验和不匹配，拒绝安装 · checksum mismatch for $asset — refusing to install"
	ok "校验通过 · Checksum verified"
}

build_from_source() {
	local goarch=$ARCH goarm="" ref url
	[ "$ARCH" = armv7 ] && goarch=arm goarm=7
	local go="$TMP/go/bin/go"
	if command -v go >/dev/null && GOTOOLCHAIN=local go version 2>/dev/null | grep -Eq 'go1\.(2[6-9]|[3-9][0-9])'; then
		go=$(command -v go)
		info "使用已安装的 Using installed $(GOTOOLCHAIN=local go version | awk '{print $3}')"
	else
		info "下载 Go $GO_VERSION（临时，用完删除）· Downloading Go $GO_VERSION (temporary, removed afterwards)"
		local tgz="go$GO_VERSION.linux-$goarch.tar.gz" fetched=0
		for site in https://go.dev/dl https://golang.google.cn/dl https://dl.google.com/go; do
			if fetch "$site/$tgz" "$TMP/$tgz" 2>/dev/null; then
				fetched=1
				break
			fi
		done
		[ $fetched = 1 ] || die "无法下载 Go 工具链 · could not download Go toolchain"
		if fetch "https://dl.google.com/go/$tgz.sha256" "$TMP/go.sha256" 2>/dev/null; then
			echo "$(cat "$TMP/go.sha256")  $TMP/$tgz" | sha256sum -c --quiet - || die "Go 工具链校验和不匹配 · Go toolchain checksum mismatch"
		else
			warn "拿不到 Go 校验和，继续（下载已经过 TLS 验证）· could not fetch Go checksum; continuing (TLS-verified download)"
		fi
		tar -C "$TMP" -xzf "$TMP/$tgz"
	fi
	if [ "$VERSION" = latest ]; then ref=heads/main; else ref=tags/$VERSION; fi
	url="https://github.com/$REPO/archive/refs/$ref.tar.gz"
	info "下载源码 Fetching source ($ref)"
	fetch "$url" "$TMP/src.tgz" || die "无法下载源码 could not download source: $url"
	mkdir -p "$TMP/src"
	tar -C "$TMP/src" --strip-components=1 -xzf "$TMP/src.tgz"
	local ver=$VERSION
	[ "$ver" = latest ] && ver="main-$(date -u +%Y%m%d)"
	info "编译中（小 VPS 上要一分钟左右）· Building (takes a minute on small VPSes)"
	(cd "$TMP/src" && HOME="$TMP" GOPATH="$TMP/gopath" GOCACHE="$TMP/gocache" GOFLAGS=-mod=readonly \
		GOTOOLCHAIN=local CGO_ENABLED=0 GOARCH=$goarch GOARM=$goarm \
		"$go" build -trimpath -ldflags "-s -w -X main.version=$ver" -o "$TMP/vivant" .) ||
		die "编译失败 · build failed"
	ok "已从源码编译 · Built from source"
}

get_binary() {
	TMP=$(mktemp -d)
	if [ $FROM_SOURCE = 1 ]; then
		build_from_source
	elif ! download_release; then
		warn "下载发布版失败，改为从源码编译 · release download failed — building from source instead"
		build_from_source
	fi
	chmod 0755 "$TMP/vivant"
	"$TMP/vivant" -version >/dev/null || die "下载的程序无法在这台系统上运行 · downloaded binary doesn't run on this system"
}

# ----------------------------------------------------------------- unit -----
print_unit() {
	cat <<'EOF'
[Unit]
Description=VIVANT (anonymous SSH chat)
Documentation=https://github.com/Freecode100Year/vivant
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# DynamicUser creates a throwaway unprivileged user; StateDirectory keeps the
# SSH host key in /var/lib/vivant across restarts and upgrades.
DynamicUser=yes
StateDirectory=vivant
WorkingDirectory=/var/lib/vivant
Environment=VIVANT_ADDR=:2222
EnvironmentFile=-/etc/vivant.env
ExecStart=/usr/local/bin/vivant -addr ${VIVANT_ADDR} -host-key /var/lib/vivant/host_ed25519 $VIVANT_ARGS
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
# VIVANT 配置 · configuration. 改完执行 After editing:  systemctl restart vivant
# 全部参数 Full flag list:  vivant -h

VIVANT_ADDR=:${PORT:-2222}

# 只返回计数的健康检查，status/doctor 用，只监听本机 · Counts-only health endpoint (status/doctor), localhost only.
VIVANT_HEALTH_ADDR=127.0.0.1:2223

# Go 运行时软内存上限（按本机内存设置）· Soft memory target for the Go runtime (from this machine's RAM).
GOMEMLIMIT=$(mem_limit)

# 公共房间，第一个是大厅；留空用内置默认 · Public rooms, first one is the lobby; empty = built-in defaults.
#VIVANT_ROOMS=general:大厅 Lobby,chinese:中文频道,english:English,technology:技术,anonymous:随便聊

# 重启后保留封禁（install.sh --persist-bans）。默认关闭：会把地址哈希密钥存盘。
# Keep bans across restarts (install.sh --persist-bans). Off by default: stores the address-hash key on disk.
#VIVANT_BAN_FILE=/var/lib/vivant/bans.json

# 进入时额外显示的公告（文本文件）· Extra welcome lines shown on connect (a text file).
#VIVANT_MOTD=/etc/vivant/motd.txt

# 其他参数 · Any other flags.
VIVANT_ARGS=-history 50 -history-ttl 1h -max-per-ip 3 -idle-timeout 1h
EOF
		chmod 0644 "$ENV_FILE"
		ok "已创建 Created $ENV_FILE"
	elif [ -n "$PORT" ]; then
		set_env VIVANT_ADDR ":$PORT"
	fi
	[ -n "$ROOMS" ] && set_env VIVANT_ROOMS "$ROOMS"
	# not read by vivant; lets "vivant-ctl doctor" check DNS for this name
	[ -n "$DOMAIN" ] && set_env VIVANT_DOMAIN "$DOMAIN"
	case "$ALT_PORT" in
	"") ;;
	off) sed -i '/^VIVANT_ALT_ADDR=/d' "$ENV_FILE" ;;
	*) set_env VIVANT_ALT_ADDR ":$ALT_PORT" ;;
	esac
	case "$PERSIST_BANS" in
	on) set_env VIVANT_BAN_FILE "$STATE_DIR/bans.json" ;;
	off)
		sed -i '/^VIVANT_BAN_FILE=/d' "$ENV_FILE"
		rm -f /var/lib/private/vivant/ban.key /var/lib/private/vivant/bans.json "$STATE_DIR/ban.key" "$STATE_DIR/bans.json"
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
		github:*) keys=$(fetch_stdout "https://github.com/${spec#github:}.keys") || die "拿不到公钥 could not fetch keys for $spec" ;;
		ssh-* | ecdsa-* | sk-*) keys=$spec ;;
		*) [ -f "$spec" ] || die "找不到管理员公钥文件 admin key file not found: $spec"; keys=$(cat "$spec") ;;
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
	set_env VIVANT_ADMIN_KEYS "$ADMIN_FILE"
	ok "管理员公钥：新增 $n 个，共 $(grep -c . "$ADMIN_FILE") 个 · Admin keys: $n added, $(grep -c . "$ADMIN_FILE") total ($ADMIN_FILE)"
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
		ufw allow "$p/tcp" comment "vivant" >/dev/null && ok "ufw：已放行 allowed $p/tcp"
		return
	fi
	if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd -q --permanent --add-port="$p/tcp" && firewall-cmd -q --reload && ok "firewalld：已放行 allowed $p/tcp"
		return
	fi
	if command -v iptables >/dev/null && iptables -S INPUT 2>/dev/null | grep -Eq -- '-P INPUT DROP|-j (REJECT|DROP)'; then
		# e.g. Oracle Cloud images ship a REJECT-all rule at the end of INPUT
		local changed=0 t
		for t in iptables ip6tables; do
			command -v $t >/dev/null || continue
			if ! $t -C INPUT -p tcp --dport "$p" -j ACCEPT 2>/dev/null; then
				$t -I INPUT 1 -p tcp --dport "$p" -m comment --comment vivant -j ACCEPT 2>/dev/null && changed=1
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
				warn "iptables 规则已添加但没有持久化，重启后会丢失 · iptables rule added but not persisted; it will be lost on reboot"
			fi
			ok "iptables：已放行 allowed $p/tcp"
		fi
		return
	fi
	if command -v nft >/dev/null && nft list ruleset 2>/dev/null | grep -q "policy drop"; then
		warn "nftables 默认丢弃，请自己放行 TCP $p（如 /etc/nftables.conf）· nftables has a drop policy; allow TCP $p yourself"
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
		for c in vivant private-talk-terminal; do
			while $t -D INPUT -p tcp --dport "$p" -m comment --comment "$c" -j ACCEPT 2>/dev/null; do :; done
		done
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
		info "安装 Tor · Installing Tor"
		pkg_install tor || die "无法安装 tor 软件包，请自己安装后再加 --tor 运行 · could not install the 'tor' package; install it yourself and rerun with --tor"
	fi
	[ -f "$TORRC" ] || touch "$TORRC"
	if ! grep -q "BEGIN vivant" "$TORRC"; then
		printf '\n# BEGIN vivant (managed by install.sh)\nHiddenServiceDir %s/\nHiddenServicePort 22 %s\n# END vivant\n' \
			"$ONION_DIR" "$ONION_LOCAL" >>"$TORRC"
	fi
	set_env VIVANT_ONION_ADDR "$ONION_LOCAL"
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
	[ -s "$ONION_DIR/hostname" ] || die "Tor 没有创建洋葱服务 · Tor did not create the onion service; check: journalctl -u tor@default -u tor"
	ok "洋葱服务 Onion service: $(cat "$ONION_DIR/hostname")"
}

remove_tor() {
	if [ -f "$TORRC" ] && grep -q "BEGIN vivant" "$TORRC"; then
		sed -i '/^# BEGIN vivant/,/^# END vivant/d' "$TORRC"
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
		if systemctl is-active --quiet vivant && port_open "$1"; then
			sleep 1 # make sure it stays up (e.g. not crash-looping on bad flags)
			systemctl is-active --quiet vivant && return 0
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

${C_G}${C_B}VIVANT 已在运行 · VIVANT is running.${C_0}  ($("$BIN" -version))

  连接 Connect:          ${C_B}ssh ${portflag}guest@$host${C_0}
  纯文本 Plain mode:     ssh ${portflag}plain@$host
EOF
	local alt
	alt=$(current_alt_port)
	if [ -n "$alt" ]; then
		echo "  被防火墙拦住 Firewalled?  ssh -p $alt guest@$host  （备用端口 alternate port）"
	fi
	local onion
	onion=$(onion_name)
	if [ -n "$onion" ]; then
		echo "  通过 Tor Via Tor:      ${C_B}torsocks ssh guest@$onion${C_0}"
		echo "                         (或 or: ssh -o ProxyCommand='nc -x 127.0.0.1:9050 %h %p' guest@$onion)"
	fi
	[ -n "$fp" ] && echo "  主机密钥 Host key:     $fp  （用户首次连接时会看到 users see this on first connect）"
	cat <<EOF
  配置 Config:           $ENV_FILE  （改完执行 then: systemctl restart vivant）
  日志 Logs:             journalctl -u vivant -f
  管理 Manage:           sudo vivant-ctl status | doctor | upgrade | uninstall

  提醒 Reminders
   • 云厂商防火墙（Oracle VCN、AWS/GCP 安全组等）也要放行入站 TCP $PORT。
     Cloud firewalls (Oracle VCN security list, AWS/GCP security groups, …) must also allow inbound TCP $PORT.
   • 域名在 Cloudflare 上要设为 ${C_B}仅 DNS（灰云）${C_0}，SSH 过不了代理。
     On Cloudflare, the DNS record must be ${C_B}DNS only${C_0} (grey cloud) — SSH can't go through the proxy.
   • 请备份 $STATE_DIR/host_ed25519，换了密钥用户会看到「主机已变更」警告。
     Back up $STATE_DIR/host_ed25519; a new key makes clients warn about a changed host.
EOF
}

# Keep a copy of this script as "vivant-ctl" so status/upgrade/uninstall work
# later even when it was run via "curl | bash".
install_ctl() {
	local src="${BASH_SOURCE[0]:-}"
	if [ -n "$src" ] && [ -f "$src" ] && grep -q "VIVANT — one-click installer" "$src" 2>/dev/null; then
		[ "$(readlink -f "$src")" = "$CTL" ] || install -m 0755 "$src" "$CTL"
	elif fetch "$RAW_URL" "$TMP/ctl.sh" 2>/dev/null && grep -q "one-click installer" "$TMP/ctl.sh"; then
		install -m 0755 "$TMP/ctl.sh" "$CTL"
	else
		return 0
	fi
	ok "管理命令 Management command: vivant-ctl (status | doctor | upgrade | uninstall)"
}

# ------------------------------------------------------------ migration -----
# The project was called Private-Talk Terminal (binary "ptterm") before
# v0.9.0. migrate_legacy moves such an install over to the new names and
# keeps everything users can notice: the host key (no "host key changed"
# warning), the onion address, bans, admins and configuration.
LEGACY_UNIT=/etc/systemd/system/ptterm.service
migrate_legacy() {
	[ -f "$LEGACY_UNIT" ] || [ -x /usr/local/bin/ptterm ] || return 0
	info "发现旧版 Private-Talk Terminal，迁移到 VIVANT · Found Private-Talk Terminal, migrating to VIVANT"
	systemctl disable --now ptterm >/dev/null 2>&1 || true
	# state: DynamicUser keeps the data in /var/lib/private/<name>, /var/lib/<name> is a symlink
	if [ -d /var/lib/private/ptterm ] && [ ! -e /var/lib/private/vivant ]; then
		mv /var/lib/private/ptterm /var/lib/private/vivant
		rm -f /var/lib/ptterm
	elif [ -d /var/lib/ptterm ] && [ ! -L /var/lib/ptterm ] && [ ! -e "$STATE_DIR" ]; then
		mv /var/lib/ptterm "$STATE_DIR"
	fi
	if [ -d /etc/ptterm ] && [ ! -e "$CONF_DIR" ]; then
		mv /etc/ptterm "$CONF_DIR"
	fi
	if [ -f /etc/ptterm.env ] && [ ! -e "$ENV_FILE" ]; then
		sed -e 's/^PTT_/VIVANT_/' -e 's/^#PTT_/#VIVANT_/' -e 's#/etc/ptterm/#/etc/vivant/#g' -e 's#/var/lib/ptterm/#/var/lib/vivant/#g' \
			-e 's#systemctl restart ptterm#systemctl restart vivant#' -e '/^#\?PTT_LANG=/d' /etc/ptterm.env >"$ENV_FILE"
		chmod 0644 "$ENV_FILE"
		rm -f /etc/ptterm.env
	fi
	# onion service: same key, same address
	if [ -f "$TORRC" ] && grep -q "BEGIN private-talk-terminal" "$TORRC"; then
		if [ -d /var/lib/tor/ptterm ] && [ ! -e "$ONION_DIR" ]; then
			mv /var/lib/tor/ptterm "$ONION_DIR"
		fi
		sed -i -e 's/BEGIN private-talk-terminal/BEGIN vivant/' -e 's/END private-talk-terminal/END vivant/' \
			-e 's#/var/lib/tor/ptterm/#/var/lib/tor/vivant/#' "$TORRC"
		systemctl restart tor >/dev/null 2>&1 || true
		systemctl cat tor@default.service >/dev/null 2>&1 && systemctl restart tor@default >/dev/null 2>&1 || true
	fi
	rm -f "$LEGACY_UNIT" /usr/local/bin/ptterm
	rm -rf /etc/systemd/system/ptterm.service.d
	systemctl daemon-reload
	# old habit keeps working
	rm -f /usr/local/sbin/ptterm-ctl
	ln -s "$CTL" /usr/local/sbin/ptterm-ctl
	ok "迁移完成：主机密钥、配置、封禁、洋葱地址都已保留 · Migrated: host key, config, bans and onion address kept"
}

# ------------------------------------------------------------- commands -----
do_install() {
	need_root
	need_systemd
	detect_arch
	migrate_legacy
	local was_installed=0
	[ -x "$BIN" ] && systemctl list-unit-files vivant.service >/dev/null 2>&1 && was_installed=1

	local want=${PORT:-$(current_port)}
	want=${want:-2222}
	local owner
	owner=$(port_owner "$want")
	if [ -n "$owner" ] && [ "$owner" != vivant ]; then
		if [ "$want" = 22 ] && [[ "$owner" == sshd* ]]; then
			die "22 端口被系统 sshd 占用。先把 sshd 移到别的端口（保持一个会话不要断！），再加 --port 22 运行 · port 22 is used by your system sshd. Move sshd to another port first (keep a session open!), then rerun with --port 22."
		fi
		die "端口 $want 已被 '$owner' 占用，用 --port N 换一个 · port $want is already in use by '$owner'. Choose another with --port N."
	fi

	if [ -n "$ALT_PORT" ] && [ "$ALT_PORT" != off ]; then
		[ "$ALT_PORT" = "$want" ] && die "--alt-port 不能和主端口相同 · must differ from the main port ($want)"
		owner=$(port_owner "$ALT_PORT")
		if [ -n "$owner" ] && [ "$owner" != vivant ]; then
			die "端口 $ALT_PORT 已被 '$owner' 占用（443 上有网站？），换一个 --alt-port 或不用 · port $ALT_PORT is already in use by '$owner' (a web server on 443?). Pick another --alt-port, or skip it."
		fi
		if [ "$ALT_PORT" -lt 1024 ]; then
			warn "端口 $ALT_PORT 会一直被扫描器探测（30 秒内断开、同样受每地址限额约束）；用户用不上就别开：--alt-port off"
			warn "port $ALT_PORT is scanned constantly by bots (dropped after 30 s, limited per address); keep it only if users need it: --alt-port off"
		fi
	fi

	get_binary
	install -m 0755 "$TMP/vivant" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
	ok "已安装 Installed $BIN ($("$BIN" -version))"

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

	systemctl enable vivant >/dev/null 2>&1
	if [ $was_installed = 1 ]; then
		systemctl restart vivant
	else
		systemctl start vivant
	fi
	if ! wait_listening "$PORT"; then
		journalctl -u vivant -n 20 --no-pager >&2 || true
		die "服务没有启动，见上面的日志 · service did not start; see the log above"
	fi
	ok "服务已在端口 $PORT 运行 · Service running on port $PORT"
	summary
}

do_upgrade() {
	[ -x "$BIN" ] || [ -x /usr/local/bin/ptterm ] || die "还没有安装 · not installed yet — run: install.sh install"
	VERSION=${VERSION:-latest}
	local before
	before=$("$BIN" -version 2>/dev/null || echo unknown)
	do_install
	info "已升级 Upgraded: $before → $("$BIN" -version)"
}

do_uninstall() {
	need_root
	local p
	p=$(current_port || true)
	systemctl disable --now vivant >/dev/null 2>&1 || true
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
	[ -L /usr/local/sbin/ptterm-ctl ] && rm -f /usr/local/sbin/ptterm-ctl
	if [ $PURGE = 1 ]; then
		rm -rf "$ENV_FILE" "$CONF_DIR" "$STATE_DIR" /var/lib/private/vivant
		ok "已删除服务、程序、配置和主机密钥 · Removed service, binary, configuration and host key"
	else
		ok "已删除服务和程序（保留了 $ENV_FILE、$CONF_DIR 和主机密钥，--purge 可一并删除）· Removed service and binary (kept config and host key; --purge deletes them)"
	fi
}

do_status() {
	if ! [ -x "$BIN" ]; then
		echo "VIVANT 还没有安装 · VIVANT is not installed."
		exit 1
	fi
	echo "版本 Version:   $("$BIN" -version)"
	echo "服务 Service:   $(systemctl is-active vivant 2>/dev/null || true) ($(systemctl is-enabled vivant 2>/dev/null || true))"
	echo "端口 Port:      $(current_port || echo 2222)$( [ -n "$(current_alt_port)" ] && echo "（另有 also $(current_alt_port)）")"
	local onion
	onion=$(onion_name)
	if [ -n "$onion" ]; then echo "洋葱 Onion:     $onion (Tor)"; fi
	if command -v ssh-keygen >/dev/null && [ -f "$STATE_DIR/host_ed25519" ]; then
		echo "主机密钥 Host key: $(ssh-keygen -lf "$STATE_DIR/host_ed25519" 2>/dev/null | awk '{print $2}')（公布出来供用户核对 · publish it so users can verify）"
	fi
	local h
	h=$(sed -n 's/^VIVANT_HEALTH_ADDR=//p' "$ENV_FILE" 2>/dev/null | tail -1)
	if [ -n "$h" ] && command -v curl >/dev/null; then
		echo "健康 Health:    $(curl -fsS --max-time 3 "http://$h/healthz" 2>/dev/null || echo "无响应 unreachable")"
	fi
	journalctl -u vivant -n 5 --no-pager 2>/dev/null || true
}

# --------------------------------------------------------------- doctor -----
D_FAIL=0 D_WARN=0
d_ok() { printf '  %s✓%s %s\n' "$C_G" "$C_0" "$*"; }
d_warn() { printf '  %s!%s %s\n' "$C_Y" "$C_0" "$*"; D_WARN=$((D_WARN + 1)); }
d_fail() { printf '  %s✗%s %s\n' "$C_R" "$C_0" "$*"; D_FAIL=$((D_FAIL + 1)); }
d_fix() { printf '      → %s\n' "$*"; }
d_head() { printf '\n%s%s%s\n' "$C_B" "$*" "$C_0"; }

env_get() { sed -n "s/^$1=//p" "$ENV_FILE" 2>/dev/null | tail -1; }

ip4_num() { # dotted quad -> integer
	local IFS=. a b c d
	read -r a b c d <<<"$1"
	echo $(((a << 24) + (b << 16) + (c << 8) + d))
}

is_cloudflare_ip() { # Cloudflare proxy ranges (https://www.cloudflare.com/ips/)
	local ip=$1 net len n m
	case "$ip" in
	2606:4700:* | 2400:cb00:* | 2803:f800:* | 2405:b500:* | 2405:8100:* | 2a06:98c0:* | 2c0f:f248:*) return 0 ;;
	*:*) return 1 ;;
	esac
	n=$(ip4_num "$ip")
	for net in 173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22 141.101.64.0/18 108.162.192.0/18 \
		190.93.240.0/20 188.114.96.0/20 197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 \
		104.24.0.0/14 172.64.0.0/13 131.0.72.0/22; do
		len=${net#*/}
		m=$(((0xFFFFFFFF << (32 - len)) & 0xFFFFFFFF))
		[ $((n & m)) -eq $(($(ip4_num "${net%/*}") & m)) ] && return 0
	done
	return 1
}

firewall_allows() { # firewall_allows PORT — 0 allowed, 1 blocked, 2 unknown/no firewall
	local p=$1
	if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
		ufw status 2>/dev/null | grep -Eq "^$p(/tcp)?[[:space:]].*ALLOW" && return 0
		return 1
	fi
	if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd --list-ports 2>/dev/null | grep -qw "$p/tcp" && return 0
		[ "$p" = 22 ] && firewall-cmd --list-services 2>/dev/null | grep -qw ssh && return 0
		return 1
	fi
	if command -v iptables >/dev/null && iptables -S INPUT 2>/dev/null | grep -Eq -- '-P INPUT DROP|-j (REJECT|DROP)'; then
		iptables -S INPUT 2>/dev/null | grep -Eq -- "--dport $p .*-j ACCEPT" && return 0
		return 1
	fi
	return 2
}

do_doctor() {
	local port alt onion_addr domain health v latest p r
	echo "${C_B}VIVANT 自检 doctor${C_0}"

	d_head "服务 Service"
	if ! [ -x "$BIN" ]; then
		d_fail "没有安装 not installed ($BIN missing)"
		d_fix "sudo bash install.sh"
		echo
		exit 1
	fi
	v=$("$BIN" -version 2>/dev/null | awk '{print $2}')
	d_ok "程序 binary $BIN ($v)"
	if systemctl is-active --quiet vivant 2>/dev/null; then
		r=$(systemctl show -p NRestarts --value vivant 2>/dev/null || true)
		if [ "${r:-0}" -gt 0 ]; then
			d_warn "服务在运行，但已重启 $r 次 · service running, but it has restarted $r time(s)"
			d_fix "journalctl -u vivant -n 50 --no-pager"
		else
			d_ok "服务在运行 · service running"
		fi
	else
		d_fail "服务状态 service is $(systemctl is-active vivant 2>/dev/null || echo unknown)"
		d_fix "sudo systemctl restart vivant; journalctl -u vivant -n 50 --no-pager"
	fi
	systemctl is-enabled --quiet vivant 2>/dev/null && d_ok "开机自启 · starts on boot" || {
		d_warn "没有开机自启 · not enabled at boot"
		d_fix "sudo systemctl enable vivant"
	}
	health=$(env_get VIVANT_HEALTH_ADDR)
	if [ -n "$health" ] && command -v curl >/dev/null; then
		if r=$(curl -fsS --max-time 3 "http://$health/healthz" 2>/dev/null); then
			d_ok "健康检查 health endpoint: $r"
		else
			d_warn "健康检查 $health 无响应 · health endpoint not answering"
		fi
	fi

	d_head "端口与防火墙 Ports and firewall"
	port=$(current_port)
	port=${port:-2222}
	alt=$(current_alt_port)
	for p in $port $alt; do
		if port_open "$p"; then
			r=$(port_owner "$p")
			if [ "$r" = vivant ] || [ "$r" = unknown ]; then
				d_ok "正在监听 listening on TCP $p"
			else
				d_fail "TCP $p 被 $r 占用 · held by $r, not vivant"
				d_fix "换一个端口 pick another port: sudo vivant-ctl install --port N"
			fi
		else
			d_fail "TCP $p 没有在监听 · nothing listening on TCP $p"
			d_fix "journalctl -u vivant -n 50 --no-pager"
		fi
		r=0
		firewall_allows "$p" || r=$?
		case $r in
		0) d_ok "本机防火墙已放行 local firewall allows $p/tcp" ;;
		1)
			d_fail "本机防火墙拦截了 local firewall blocks $p/tcp"
			d_fix "sudo vivant-ctl install（会重新放行 reopens it），或自己放行 or allow $p/tcp yourself"
			;;
		*) d_ok "没发现拦截 $p/tcp 的本机防火墙 · no blocking local firewall found" ;;
		esac
	done
	for p in $port $alt; do
		if [ "$p" -lt 1024 ] && [ "$p" != 22 ]; then
			d_warn "TCP $p 是知名端口，会一直被扫描（30 秒内断开、受每地址限额约束）· well-known port: bots scan it constantly"
			[ "$p" = "$alt" ] && d_fix "用户用不上？· not needed? sudo vivant-ctl install --alt-port off"
		fi
	done
	if [ "$port" = 22 ] || [ "$alt" = 22 ]; then
		d_warn "聊天室用的是 22 端口，确认你自己的管理 SSH 在别的端口 · the chat uses port 22 — make sure your admin SSH runs elsewhere"
	fi
	echo "      云厂商防火墙（安全组、Oracle VCN 等）在这里看不到，请从别的网络实际连一次："
	echo "      Cloud firewalls (security groups, Oracle VCN…) can't be seen from here; test from another network:"
	echo "      ssh -p $port guest@<this server>"

	onion_addr=$(env_get VIVANT_ONION_ADDR)
	if [ -n "$onion_addr" ]; then
		d_head "Tor"
		if systemctl is-active --quiet tor@default 2>/dev/null || systemctl is-active --quiet tor 2>/dev/null; then
			d_ok "tor 在运行 · tor running"
		else
			d_fail "tor 没有运行 · tor is not running"
			d_fix "sudo systemctl restart tor; journalctl -u tor@default -n 30"
		fi
		if [ -n "$(onion_name)" ]; then d_ok "洋葱地址 onion address $(onion_name)"; else
			d_fail "还没有洋葱地址 · no onion address yet"
			d_fix "sudo vivant-ctl install --tor"
		fi
		port_open "${onion_addr##*:}" && d_ok "本地洋葱监听 local onion listener ${onion_addr}" || d_fail "${onion_addr} 没有在监听 · nothing listening"
	fi

	d_head "域名 DNS"
	domain=${DOMAIN:-$(env_get VIVANT_DOMAIN)}
	if [ -z "$domain" ]; then
		echo "      不知道域名 · no domain known — run: vivant-ctl doctor --domain chat.example.com"
	elif ! command -v getent >/dev/null; then
		d_warn "缺少 getent，无法解析 · getent missing, can't resolve $domain"
	else
		local addrs mine cf=0 match=0 a
		addrs=$(getent ahosts "$domain" 2>/dev/null | awk '{print $1}' | sort -u || true)
		if [ -z "$addrs" ]; then
			d_fail "$domain 解析不到 · does not resolve"
			d_fix "添加指向本机的 A/AAAA 记录 · add an A (and/or AAAA) record pointing to this server"
		else
			mine=$( (
				public_ip || true
				fetch_stdout https://api6.ipify.org 2>/dev/null || true
				echo
				hostname -I 2>/dev/null || true
			) | tr ' ' '\n' | sort -u)
			for a in $addrs; do
				is_cloudflare_ip "$a" && cf=1
				grep -qxF "$a" <<<"$mine" && match=1
			done
			if [ $cf = 1 ]; then
				d_fail "$domain 解析到了 Cloudflare 代理，SSH 过不去 · resolves to Cloudflare's proxy ($(echo "$addrs" | tr '\n' ' ')) — SSH can't pass through it"
				d_fix "Cloudflare 后台 → DNS → 把这条记录改成「仅 DNS」（灰云）· set this record to \"DNS only\" (grey cloud)"
			elif [ $match = 1 ]; then
				d_ok "$domain → $(echo "$addrs" | tr '\n' ' ')（本机 this server）"
			else
				d_warn "$domain → $(echo "$addrs" | tr '\n' ' ')— 不是本机地址 · not an address of this server"
				d_fix "把 A/AAAA 记录指向 $(public_ip)（除非有 NAT 或负载均衡转发）· point the record at this server unless a NAT/load balancer forwards it"
			fi
		fi
	fi

	d_head "主机密钥 Host key"
	local hk=$STATE_DIR/host_ed25519
	if [ -f "$hk" ]; then
		local mode
		mode=$(stat -Lc %a "$hk" 2>/dev/null || echo "?")
		if [ "$mode" = 600 ] || [ "$mode" = 400 ]; then
			d_ok "存在，权限 $mode · present, mode $mode"
		else
			d_warn "权限 $mode，应为 600 · mode $mode — should be 600"
			d_fix "sudo chmod 600 $hk"
		fi
		command -v ssh-keygen >/dev/null && d_ok "指纹 fingerprint $(ssh-keygen -lf "$hk" 2>/dev/null | awk '{print $2}')（公布出来供用户核对 · publish it so users can verify）"
		echo "      请备份（丢了密钥，所有用户都会看到「主机密钥已变更」警告）· Back it up (a lost key makes every client warn):"
		echo "        sudo install -m 600 $hk ~/vivant-host-key.backup"
	else
		d_warn "还没有主机密钥（首次启动时生成）· no host key yet (created on first start)"
	fi
	local bf
	bf=$(env_get VIVANT_BAN_FILE)
	if [ -n "$bf" ] && [ -f "$bf" ]; then
		mode=$(stat -Lc %a "$bf" 2>/dev/null || echo "?")
		[ "$mode" = 600 ] && d_ok "封禁列表 ban list $bf mode 600" || d_warn "封禁列表 ban list $bf 权限 mode $mode，应为 should be 600"
	fi

	d_head "更新 Updates"
	latest=$(fetch_stdout "https://raw.githubusercontent.com/$REPO/main/VERSION" 2>/dev/null | tr -d ' \n' || true)
	if ! [[ "$latest" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
		d_warn "无法检查更新（连不上 GitHub？）· couldn't check for updates (no access to GitHub?)"
	elif ! [[ "${v#v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
		d_ok "开发版 development build $v（最新发布 latest release: v$latest）"
	elif [ "${v#v}" = "$latest" ]; then
		d_ok "已是最新 up to date (v$latest)"
	elif [ "$(printf '%s\n%s\n' "${v#v}" "$latest" | sort -V | tail -1)" = "$latest" ]; then
		d_warn "有新版本 v$latest available（已安装 installed: $v）"
		d_fix "sudo vivant-ctl upgrade"
	else
		d_ok "已安装的 $v 比最新发布 v$latest 还新 · installed is newer than the latest release"
	fi

	echo
	if [ $D_FAIL -gt 0 ]; then
		echo "${C_R}${D_FAIL} 个问题 problem(s)${C_0}，${D_WARN} 个提醒 warning(s)。"
		exit 1
	fi
	echo "${C_G}没有发现问题 No problems found${C_0}，${D_WARN} 个提醒 warning(s)。"
}

main() {
	parse_args "$@"
	case "$CMD" in
	install) do_install ;;
	upgrade) do_upgrade ;;
	uninstall) do_uninstall ;;
	status) do_status ;;
	doctor) do_doctor ;;
	print-unit) print_unit ;;
	help) usage ;;
	esac
}

main "$@"
