#!/usr/bin/env bash
# Builds the offline one-click installer from a dist/ directory that already
# holds vivant-linux-{amd64,arm64,armv7} and install.sh:
#
#   deploy/make-offline-bundle.sh dist v0.9.2   ->  dist/vivant-v0.9.2-linux-offline.sh
#
# The result is one self-extracting file: deploy/offline-header.sh followed by
# a tar.gz of the three binaries, the installer, SHA256SUMS and README.txt.
# 离线一键安装包：一个自解压文件，包含三种架构的程序、安装脚本和校验文件。
set -euo pipefail
dist=${1:?usage: make-offline-bundle.sh DIST_DIR VERSION}
ver=${2:?usage: make-offline-bundle.sh DIST_DIR VERSION}
name=vivant-$ver-linux-offline
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/$name"
for f in vivant-linux-amd64 vivant-linux-arm64 vivant-linux-armv7 install.sh; do
	[ -f "$dist/$f" ] || { echo "missing $dist/$f" >&2; exit 1; }
	cp "$dist/$f" "$work/$name/"
done
chmod 0755 "$work/$name"/vivant-linux-* "$work/$name/install.sh"
(cd "$work/$name" && sha256sum vivant-linux-* install.sh >SHA256SUMS)
cat >"$work/$name/README.txt" <<TXT
VIVANT $ver — 离线安装包 · offline install bundle

适用于连不上 GitHub 的服务器（x86_64、ARM64、ARMv7 的 systemd Linux）。
For servers that can't reach GitHub (x86_64, ARM64, ARMv7 Linux with systemd).

一键安装 · One-click install:
  在服务器上 · on the server:          sudo bash $name.sh
  在自己电脑上通过 SSH · from your PC:  bash $name.sh --to root@SERVER
参数和在线安装一样 · Same options as the online installer, e.g.:
  sudo bash $name.sh --port 2222 --admin-key /root/admin.pub
已经解压出来的话也可以直接运行 · From these unpacked files:  sudo ./install.sh

安装脚本会直接使用同目录里对应架构的程序，并先用 SHA256SUMS 校验，不联网。
The installer uses the matching binary in this directory, checks it against
SHA256SUMS first, and needs no network.

需要联网的选项 · Options that still need the network:
  --tor                  要从软件源安装 tor（已装好则不用）· installs tor from the package repositories (unless already installed)
  --admin-key github:…   要从 GitHub 拉公钥，离线时请用公钥文件 · fetches keys from GitHub; offline, pass a .pub file

升级：下载新的离线包，同样运行一次，配置和主机密钥会保留。
Upgrading: get the new bundle and run ./install.sh again; config and host key are kept.
TXT
tar -C "$work" -czf "$work/payload.tgz" "$name"
sum=$(sha256sum "$work/payload.tgz" | awk '{print $1}')
here=$(cd "$(dirname "$0")" && pwd)
sed -e "s/@VERSION@/$ver/g" -e "s/@PAYLOAD_SHA256@/$sum/" "$here/offline-header.sh" >"$dist/$name.sh"
cat "$work/payload.tgz" >>"$dist/$name.sh"
chmod 0755 "$dist/$name.sh"
echo "$dist/$name.sh"
