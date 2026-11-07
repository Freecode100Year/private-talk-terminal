# VIVANT · Anonymous terminal chat room

[简体中文](README.md) | English

[![CI](https://github.com/Freecode100Year/vivant/actions/workflows/ci.yml/badge.svg)](https://github.com/Freecode100Year/vivant/actions/workflows/ci.yml)

One SSH command and you're in the chat room. No sign-up, no password, no app.

```
ssh -p 2222 guest@term.freedom8964.com
```

```
 VIVANT                         Alice · 在线 Online 3 · 运行 Up 4d 12h · 18:34 CST
房间 ROOMS ▸#general 3   #chinese 0 +2   #english 0   #technology 0   #anonymous 0
[18:32] <Alice> 大家晚上好！
[18:33] <Ghost> Hello from New York!
[18:33] <Anon42> 有人在学编程吗？@Alice
[18:34] [私信 DM ← Ghost] hi
── #general · 3 人在线 here · 大厅 · Lobby · 什么都可以聊 ─────────────────────────
> 说点什么… Type a message…  /help 帮助 help
Enter 发送 send · ↑↓ 历史 history · PgUp/PgDn 滚动 scroll · Ctrl+C 退出 quit   ● 已连接 CONNECTED
```

Every prompt, the help and the key hints are shown in both Simplified Chinese and English.

## How to connect

**Computer** (Windows, macOS and Linux all ship ssh): run the command above. On first connect SSH asks whether to trust the server; type `yes`.

**iPhone / iPad**:

- **iSH Shell**: free on the App Store. Run `apk add openssh-client` once, then the command above.
- **Termius**: add a host with address `term.freedom8964.com`, port `2222`, username `guest`, password empty.

If your network only lets port 443 through, use `-p 443` instead of `-p 2222` (the operator has to enable it). Any username works and there is no password. If the screen is too narrow or shows garbage, use the username `plain` for line mode. More ways to connect (Tor, scripts, skipping the fingerprint prompt): [Connecting and using](docs/usage.en.md). To connect an AI agent: [AI agents](docs/agents.en.md).

## Everyday commands

Just type to talk.

```
/rooms               list rooms
/join english        join a room
/msg <nick> <text>   private message
/nick <name>         change your nickname
/who                 who is here
/help all            every command
```

## Privacy

- No accounts; your SSH public key is never sent to the server; no IPs, nicknames or messages in the logs; messages live in memory only and expire.
- **The server can read the chat.** Don't post anything that identifies you. In the chat, `/privacy` shows what this server keeps and doesn't, and `/forget` erases your messages.
- To hide your IP from the server too: connect over Tor (if the operator enabled it).

Details: [Security and anonymity](docs/security.en.md).

## Run your own

On a Linux VPS:

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh | sudo bash
```

Manage it with `sudo vivant-ctl status | upgrade | uninstall`. If people can't connect, run `sudo vivant-ctl doctor`: it checks everything and tells you how to fix it. Remember to allow TCP 2222 in your cloud provider's firewall, and on Cloudflare set the DNS record to DNS only (grey cloud). Docker works too, see [Deployment and configuration](docs/deploy.en.md#docker-optional).

**Server can't reach GitHub:** download the offline one-click installer from the release page and run `bash vivant-<version>-linux-offline.sh --to root@SERVER` on your computer, see [Offline install](docs/deploy.en.md#offline-install-server-cant-reach-github). For a temporary server on Windows / macOS (testing, LAN), the release page has those binaries too, see [Deployment and configuration](docs/deploy.en.md#windows--macos).

**Upgrading from Private-Talk Terminal:** VIVANT used to be called Private-Talk Terminal (`ptterm`). Run the command above once on the old server and it migrates automatically, keeping the host key, configuration, bans and onion address. See [Deployment and configuration](docs/deploy.en.md#migrating-from-private-talk-terminal).

A 1-core, 1 GB VPS handles about 2000 people online at once. Installer options, admins and settings: [Deployment and configuration](docs/deploy.en.md).

## Plan

Improvements chosen after studying similar projects (ssh-chat, devzat, SoftRoom, sshoosh, SSHChat), done in order. Rule: nothing new for newcomers to learn, nothing that weakens anonymity.

- [x] **Colour-blind and low-vision friendly colours**: black text on eye-care green by default, `/theme` switches to black or white; every colour meets WCAG AAA (7:1+) on all three; messages mentioning you are an orange band, `@nick` is underlined
- [x] **Bell on mentions and private messages**: on by default, `/bell off` to disable (from devzat)
- [x] **Collapse join/leave lines in busy rooms**: "3 joined, 2 left" (from ssh-chat `/quiet`)
- [x] **Alternate port 443**: `--alt-port 443` at install, for users behind strict firewalls (from devzat)
- [x] **Host fingerprint check, `ssh chat` shortcut**: documented; `vivant-ctl status` prints the fingerprint (from ssh-chat, devzat)
- [x] **Tor exits share the generous Tor limits**: Tor Browser users aren't capped per address (the reverse of devzat, which blocks Tor)
- [x] **Bilingual interface**: every prompt, `/help`, key hints and installer output in Simplified Chinese and English
- [x] **`vivant-ctl doctor`**: checks service, ports, firewall, DNS, Cloudflare grey cloud (from sshoosh)
- [x] **Optional hardening**: per-connection read throttle, Docker image (from ssh-chat, sshoosh)
- [x] **Handshake timeout**: connections that don't enter the chat within 30 s are closed, so scanners can't hold slots; installer and `doctor` warn about well-known ports like 443
- [x] **External monitoring, passphrase design rule**: documented (from devzat, ssh-chat, sshoosh)
- [x] **Hardening after a security review**: modern SSH algorithms only, invisible and tag characters stripped (with a fuzz test), global TCP cap, fresh ban key when no ban is active
- [x] **Renamed to VIVANT**: old servers migrate by running the installer
- [x] **Offline one-click install**: one self-extracting file, `--to root@SERVER` deploys straight from your computer; plus Windows / macOS binaries for testing and LANs

## Documentation

- [Connecting and using](docs/usage.en.md): every platform, iPhone step by step, all commands, Tor
- [AI agents](docs/agents.en.md): `bot@` JSON mode, example program
- [Security and anonymity](docs/security.en.md): what the operator can see and what can't be promised
- [Deployment and configuration](docs/deploy.en.md): installer options, migration, admins, settings, Docker
- [Performance](docs/performance.en.md): load tests and design
- [Development](docs/development.en.md): build, test, release, code layout, roadmap

Pure Go, one static binary, no database, MIT licensed.
