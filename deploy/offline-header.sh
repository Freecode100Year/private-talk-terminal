#!/usr/bin/env bash
# VIVANT offline one-click installer — self-extracting.
# VIVANT 离线一键安装包（自解压）
#
# On the server · 在服务器上:
#   sudo bash vivant-@VERSION@-linux-offline.sh [install options]
# From your own computer, over SSH · 在自己电脑上通过 SSH 部署:
#   bash vivant-@VERSION@-linux-offline.sh --to root@SERVER [install options]
# Just unpack · 只解压:
#   bash vivant-@VERSION@-linux-offline.sh --extract [DIR]
#
# Everything (binaries for x86_64/ARM64/ARMv7, installer, checksums) is in
# this one file; nothing is downloaded. 所有文件都在这一个文件里，不联网。
set -euo pipefail

VERSION=@VERSION@
PAYLOAD_SHA256=@PAYLOAD_SHA256@
NAME=vivant-$VERSION-linux-offline
SELF=$0

say() { printf '==> %s\n' "$*"; }
die() {
	printf ' ✗ %s\n' "$*" >&2
	exit 1
}

payload() { # writes the embedded tar.gz to stdout
	local line
	line=$(awk '/^__VIVANT_PAYLOAD_BELOW__$/ { print NR + 1; exit }' "$SELF")
	[ -n "$line" ] || die "安装包已损坏（找不到数据）· bundle is damaged (no payload)"
	tail -n +"$line" "$SELF"
}

extract() { # extract DIR — verify, then unpack into DIR/$NAME
	local dir=$1 sum
	command -v sha256sum >/dev/null || die "需要 sha256sum · sha256sum is required"
	sum=$(payload | sha256sum | awk '{print $1}')
	[ "$sum" = "$PAYLOAD_SHA256" ] || die "安装包校验失败，可能下载不完整或被改动 · bundle checksum mismatch (incomplete download or tampered)"
	mkdir -p "$dir"
	payload | tar -xzf - -C "$dir"
}

case "${1:-}" in
--to)
	# Copy this file to the server over SSH and install there.
	host=${2:?"用法 usage: --to user@server [install options]"}
	shift 2
	command -v scp >/dev/null && command -v ssh >/dev/null || die "需要 ssh 和 scp · ssh and scp are required"
	remote=/tmp/$NAME.$$.sh
	say "上传到 Uploading to $host"
	scp -q "$SELF" "$host:$remote"
	args=""
	# POSIX single quotes: the remote login shell may be sh/dash
	for a in "$@"; do args+=" '${a//\'/\'\\\'\'}'"; done
	say "在 $host 上安装 · Installing on $host"
	# -t: sudo may ask for a password
	ssh -t "$host" "if [ \"\$(id -u)\" = 0 ]; then bash $remote$args; else sudo bash $remote$args; fi; rc=\$?; rm -f $remote; exit \$rc"
	exit $?
	;;
--extract)
	dir=${2:-.}
	extract "$dir"
	say "已解压到 Extracted to $dir/$NAME"
	exit 0
	;;
-h | --help)
	sed -n '2,13p' "$SELF" | sed 's/^# \{0,1\}//'
	echo
	echo "安装选项 Install options:"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	extract "$tmp"
	bash "$tmp/$NAME/install.sh" help
	exit 0
	;;
esac

if [ "$(id -u)" != 0 ]; then
	command -v sudo >/dev/null || die "请用 root 运行 · please run as root"
	say "需要 root 权限，使用 sudo · root needed, using sudo"
	exec sudo bash "$SELF" "$@"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "VIVANT $VERSION 离线安装 · offline install"
extract "$tmp"
bash "$tmp/$NAME/install.sh" "$@"
exit $?
__VIVANT_PAYLOAD_BELOW__
