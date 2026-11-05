# Deployment and configuration

[← Back to README](../README.en.md) · [简体中文](deploy.md) | English

## One-line install

On a VPS (Debian, Ubuntu, the RHEL family or any other systemd Linux; x86_64, ARM64, ARMv7):

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh | sudo bash
```

With options (note the `-s --`):

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh \
  | sudo bash -s -- --port 2222 --admin-key github:YourGitHubName --domain term.freedom8964.com
```

The script:

1. Detects the CPU architecture, downloads the matching binary from GitHub Releases and **verifies its SHA256** (builds from source if the download fails)
2. Installs it as a fully sandboxed systemd service (throwaway unprivileged user, read-only system, no capabilities)
3. Creates `/etc/vivant.env`, setting `GOMEMLIMIT` from the machine's memory
4. Opens the firewall port (ufw / firewalld / iptables, including the REJECT rule Oracle Cloud images ship)
5. Checks the port is free, starts the service, confirms it's listening, then prints the connect command and the host key fingerprint
6. Installs the management command `vivant-ctl`

Afterwards:

```bash
sudo vivant-ctl status            # state + people online
sudo vivant-ctl doctor            # self-check: service, ports, firewall, DNS, Cloudflare grey cloud, host key, updates
sudo vivant-ctl upgrade           # newest release, keeps configuration and host key
sudo vivant-ctl install --port 2299 --admin-key ~/.ssh/id_ed25519.pub   # change settings (safe to re-run)
sudo vivant-ctl uninstall         # remove (keeps configuration and host key)
sudo vivant-ctl uninstall --purge # remove everything
```

All installer output is in Chinese and English.

| Installer option | Meaning |
| --- | --- |
| `--port N` | SSH port for the chat, default 2222. For 22, move the system sshd first; the script adds the port capability itself |
| `--admin-key KEY` | admin public key: `ssh-ed25519 AAAA…`, a `.pub` file path, or `github:USERNAME`; repeatable |
| `--rooms LIST` | public rooms, e.g. `"general:大厅 Lobby,chinese:中文,tech:技术"` (the first is the lobby) |
| `--domain NAME` | the domain users connect to: shown in the final connect command and used by `doctor` for DNS checks |
| `--version vX.Y.Z` | install a specific release |
| `--alt-port N` | also listen on a second port, e.g. `443`: company, school and hotel networks often allow only 80/443. Off by default. Well-known ports like 443 are probed by scanners constantly, adding noise to logs and connection counts (scanners that don't enter the chat within 30 s are dropped and limited per address like everyone else); don't open it unless your users need it. Unavailable if a web server already uses 443 — the installer checks and tells you; `--alt-port off` removes it |
| `--tor` | also publish the chat as a Tor onion service (installs Tor), so users can connect without revealing their IP; see [Security and anonymity](security.en.md) |
| `--persist-bans` | bans survive restarts and upgrades (by default they live in memory only); `--no-persist-bans` turns it off. Privacy trade-off: [Security and anonymity](security.en.md) |
| `--from-source` | build from source (downloads Go temporarily, removed afterwards) |
| `--no-firewall` | leave the firewall alone |

Restricted network: point `VIVANT_DOWNLOAD_BASE=<mirror>` at a directory with `vivant-linux-<arch>` and `SHA256SUMS`; for source builds set `GOPROXY=https://goproxy.cn`.

**Two things you still do by hand:**

- Allow the TCP port in your cloud provider's firewall (Oracle VCN security list, AWS/GCP security groups, …).
- Point the domain's A/AAAA record at the VPS. On Cloudflare it must be **DNS only (grey cloud)**; the orange-cloud proxy doesn't forward SSH.

## Offline install (server can't reach GitHub)

Every release includes an offline one-click installer, `vivant-<version>-linux-offline.sh`: a single file holding the binaries for all three architectures, the installer and checksums; it never touches the network. Download it from [Releases](https://github.com/Freecode100Year/vivant/releases) on a machine with internet access, then either:

```bash
# Option 1: deploy to the server with one command from your computer (Linux, macOS, or Git Bash / WSL on Windows)
bash vivant-v0.9.6-linux-offline.sh --to root@SERVER

# Option 2: copy the file to the server and run it there
scp vivant-v0.9.6-linux-offline.sh root@SERVER:
sudo bash vivant-v0.9.6-linux-offline.sh
```

Add the same options as the online installer, e.g. `--to root@SERVER --port 2222 --admin-key ~/.ssh/id_ed25519.pub` (with option 1 a key file path refers to the server, so copy the key there first or use option 2). `--to` uses the ssh/scp on your computer; if the server user isn't root it uses sudo.

The file first verifies itself (an incomplete download or a modified file is refused), unpacks the installer and binaries, the installer checks the binary against `SHA256SUMS` again and installs it, and the temporary files are removed. To upgrade, get the new file and run it again; configuration and host key are kept. `--extract` only unpacks; `--help` lists every option.

Only two options still need the network: `--tor` (installs tor from the package repositories, unless already installed) and `--admin-key github:USERNAME` (offline, pass a key file). On an offline server `vivant-ctl upgrade` can't reach GitHub and fails; upgrade with a new offline file instead.

## Windows / macOS

Linux is recommended for servers. To run one on Windows or a Mac for a while (local testing, a chat with friends on your LAN), the release page has `vivant-windows-amd64.exe`, `vivant-windows-arm64.exe`, `vivant-macos-amd64` and `vivant-macos-arm64`. Download and run:

```bash
./vivant-macos-arm64 -addr :2222          # macOS (Apple silicon); allow it under System Settings → Privacy & Security the first time
vivant-windows-amd64.exe -addr :2222      # Windows; allow private networks when the firewall asks
```

Others connect with `ssh -p 2222 guest@your-LAN-IP`. There's no installer, no systemd sandbox and no autostart; the host key is kept in `data/` in the current directory. Fine for testing and LANs — not recommended for the public internet.

## Migrating from Private-Talk Terminal

VIVANT used to be called Private-Talk Terminal (binary `ptterm`). On a server running the old version, run the one-line install command above once and it migrates automatically:

- The host key, configuration (`PTT_*` becomes `VIVANT_*`), admin keys, bans and Tor onion address are all kept: users see no "host key changed" warning and the onion address stays the same.
- The service becomes `vivant`, the binary `/usr/local/bin/vivant`, the config file `/etc/vivant.env` and the data directory `/var/lib/vivant`.
- The old `ptterm-ctl` command stays as a shortcut to `vivant-ctl`.

Servers that haven't migrated can still run `sudo ptterm-ctl upgrade` to get the new program (under the old names) and run the install command to migrate whenever convenient. The binary also still accepts the old `PTT_*` environment variables.

## Can't connect? Run the self-check

```bash
sudo vivant-ctl doctor --domain chat.example.com
```

It checks each item and prints the fix; the exit code is 1 when something is wrong:

- Is the service running, enabled at boot, restarting over and over?
- Is the chat port (and the alternate port) listening, taken by another program, allowed by the local firewall?
- Does the domain resolve to this server? If it resolves to Cloudflare's proxy (orange cloud), you're told to switch it to "DNS only" (grey cloud), because SSH can't pass the Cloudflare proxy
- Tor (if enabled): is tor running, the onion address, the local listener
- Does the host key exist with mode 600? Prints the fingerprint and a backup command
- Is there a newer version?

You only need `--domain` once; a `--domain` given at install time is remembered too. Cloud security groups (Oracle VCN, AWS/GCP, …) can't be seen from inside the server, so test once from another network.

## External monitoring

To be told when the chat is down, create a **TCP port** check in any external monitor (UptimeRobot, Uptime Kuma, Better Stack, …) for `your-domain:2222`, every 1–5 minutes. It only checks that the port answers — no login, no chat, nothing extra opened on the server.

The health endpoint `127.0.0.1:2223/healthz` listens on localhost only; don't expose it publicly for monitoring. For a graph of people online, read it on the server itself with cron, node_exporter or similar.

## Publish the host fingerprint

On first connect SSH shows the server's host key fingerprint. Publish it where users can see it (README, website) so they can check it matches:

```bash
sudo vivant-ctl status        # the Host key: SHA256:… line
```

The host key is `/var/lib/vivant/host_ed25519`; back it up. A new key means a new fingerprint and a warning for everyone who connected before.

## Admins

Use the username `admin` with the private key registered at install (`ssh -p 2222 -i ~/.ssh/id_ed25519 admin@HOST`); the separator line shows `[ADMIN]`. Only the username `admin` is checked against keys: a wrong admin key is refused outright instead of silently becoming a normal user.

```
/kick <nick> [reason]   /mute <nick> [minutes]   /unmute <nick>
/ban <nick> [minutes]   /bans   /unban <id>   /unbanall
/broadcast <text>       /topic <text>   /reports
```

Bans apply to an anonymous hash of the address (see [Security and anonymity](security.en.md)). `/bans` lists hash prefixes and time left, `/unban <id>` lifts one. By default bans live in memory and are cleared on restart; install with `--persist-bans` to keep them.

## Configuration

Config file `/etc/vivant.env`; after editing run `systemctl restart vivant`:

| Environment variable / flag | Default | Meaning |
| --- | --- | --- |
| `VIVANT_ADDR` / `-addr` | `:2222` | listen address |
| `VIVANT_ADMIN_KEYS` / `-admin-keys` | — | admin public keys file |
| `VIVANT_ROOMS` / `-rooms` | 5 default rooms | `name:topic,name2:topic2` |
| `VIVANT_MOTD` / `-motd` | — | text file with extra welcome lines |
| `VIVANT_HEALTH_ADDR` / `-health-addr` | `127.0.0.1:2223` | `GET /healthz`, counts only |
| `VIVANT_ADMIN_USER` / `-admin-user` | `admin` | username admins log in with |
| `VIVANT_ONION_ADDR` / `-onion-addr` | — | local listener for the Tor onion service (`--tor` sets `127.0.0.1:2224`) |
| `VIVANT_BAN_FILE` / `-ban-file` | — | ban persistence file (`--persist-bans` sets `/var/lib/vivant/bans.json`) |
| `VIVANT_ALT_ADDR` / `-alt-addr` | — | extra listen addresses, comma separated (`--alt-port 443` sets `:443`) |
| `VIVANT_TOR_EXITS` / `-tor-exits` | `on` | download Tor's exit list so Tor users share the generous limits; `off` disables |
| `-quiet-above` | 20 | above this many people, join/leave lines become a summary every 30 s (-1 = never) |
| `-history` / `-history-ttl` | 50 / 1h | messages kept per room, and for how long |
| `-max-clients` / `-max-per-ip` / `-max-tcp-per-ip` / `-conn-per-min` | 1000 / 3 / 8 / 10 | connection limits |
| `-read-rate` | 32768 | bytes per second a connection may send before it's slowed down, not dropped (burst 8×; 0 = off). Typing and pasting never get near it |
| `-msg-rate` / `-msg-burst` / `-max-len` | 1/s / 5 / 500 | posting limits |
| `-idle-timeout` | 1h | disconnect after this long without posting (0 = never) |
| `GOMEMLIMIT` | 60% of RAM | Go runtime soft memory limit |

Other flags go in `VIVANT_ARGS=`. Every flag: `vivant -h`. The old `PTT_*` names still work.

**The host key** is `/var/lib/vivant/host_ed25519`; back it up. With a new key, returning users see a "host key changed" warning.

## Docker (optional)

The one-line installer (systemd) is still the recommended way. If you prefer containers, every release pushes a multi-arch image (amd64 / arm64 / armv7) to GHCR:

```bash
docker run -d --name vivant --restart unless-stopped --network host \
  --read-only --cap-drop ALL --security-opt no-new-privileges --ulimit core=0 \
  -v vivant-data:/data -e GOMEMLIMIT=600MiB \
  ghcr.io/freecode100year/vivant:latest
```

Or use [`deploy/docker-compose.yml`](../deploy/docker-compose.yml): `docker compose -f deploy/docker-compose.yml up -d`.

- **Always use `--network host`.** With `-p 2222:2222`, Docker's port proxy (IPv6, and IPv4 on some distributions) makes every user appear to come from the same address: everyone shares one rate-limit allowance, one flooder affects all, and a ban hits everyone.
- The image is distroless — no shell — and runs as a non-root user (uid 65532); the host key is `/data/host_ed25519` in the `vivant-data` volume, so deleting the volume means a new key.
- Configure with environment variables (the `VIVANT_*` names above) or flags after the image name, e.g. `… vivant:latest -max-per-ip 3`.
- Firewall, Tor and `vivant-ctl doctor`, which the installer handles, are up to you with containers.
- Build it yourself: `docker build -t vivant .`
