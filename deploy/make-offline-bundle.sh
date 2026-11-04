#!/usr/bin/env bash
# Builds the offline install bundle from a dist/ directory that already holds
# vivant-linux-{amd64,arm64,armv7} and install.sh:
#
#   deploy/make-offline-bundle.sh dist v0.9.2   ->  dist/vivant-v0.9.2-linux-offline.tar.gz
#
# 离线安装包：把三种架构的程序、安装脚本和校验文件打成一个压缩包。
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

1. 在能上网的电脑上下载本压缩包，传到服务器 · Download this file elsewhere and copy it over:
     scp $name.tar.gz root@SERVER:
2. 在服务器上解压并安装 · On the server, unpack and install:
     tar xzf $name.tar.gz && cd $name && sudo ./install.sh
   参数和在线安装一样 · Same options as the online installer, e.g.:
     sudo ./install.sh --port 2222 --admin-key /root/admin.pub

安装脚本会直接使用同目录里对应架构的程序，并先用 SHA256SUMS 校验，不联网。
The installer uses the matching binary in this directory, checks it against
SHA256SUMS first, and needs no network.

需要联网的选项 · Options that still need the network:
  --tor                  要从软件源安装 tor（已装好则不用）· installs tor from the package repositories (unless already installed)
  --admin-key github:…   要从 GitHub 拉公钥，离线时请用公钥文件 · fetches keys from GitHub; offline, pass a .pub file

升级：下载新的离线包，同样运行 ./install.sh，配置和主机密钥会保留。
Upgrading: get the new bundle and run ./install.sh again; config and host key are kept.
TXT
tar -C "$work" -czf "$dist/$name.tar.gz" "$name"
echo "$dist/$name.tar.gz"
