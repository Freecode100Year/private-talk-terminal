# Connecting and using

[← Back to README](../README.en.md) · [简体中文](usage.md) | English

## Ways to connect

| Way | Command |
| --- | --- |
| Full interface (recommended) | `ssh -o StrictHostKeyChecking=accept-new -p 2222 guest@HOST` |
| Line mode (old terminals, garbled CMD) | `ssh -o StrictHostKeyChecking=accept-new -p 2222 plain@HOST` |
| Behind a strict firewall (if the operator opened the alternate port) | `ssh -o StrictHostKeyChecking=accept-new -p 443 guest@HOST` |
| No terminal / scripts | `ssh -o StrictHostKeyChecking=accept-new -T -p 2222 guest@HOST` |
| AI agents / bots (JSON) | `ssh -o StrictHostKeyChecking=accept-new -T -p 2222 bot@HOST` — see [AI agents](agents.en.md) |
| Through Tor (the server never sees your IP) | `torsocks ssh guest@<onion-address>.onion` (see below) |

- Any username works (`admin` is reserved for admins), there is no password, and you land straight in the chat.
- With the `accept-new` option above, the first connection records the server's host key automatically; if the key ever changes later, SSH refuses to connect and warns you.
- Your client never sends your public key to the server, so you don't need `PubkeyAuthentication=no` (why: [Security and anonymity](security.en.md)).
- Windows 10/11 ships OpenSSH; Windows Terminal displays Chinese best. If old CMD shows garbage, use `plain@` or run `chcp 65001` first.

To leave out `-o StrictHostKeyChecking=accept-new`, set it once for the host in your SSH config file: `%USERPROFILE%\.ssh\config` on Windows, `~/.ssh/config` on macOS/Linux. Put this near the top:

```sshconfig
Host term.freedom8964.com
    StrictHostKeyChecking accept-new
```

From then on `ssh -p 2222 guest@term.freedom8964.com` is enough. The key is saved on first connect and changes are still refused.

### Shorten it to `ssh chat`

If you come often, add these lines to the same file and just type `ssh chat`:

```sshconfig
Host chat
    HostName term.freedom8964.com
    Port 2222
    User guest
    StrictHostKeyChecking accept-new
```

### Check the host fingerprint

On first connect SSH shows a line like `ED25519 key fingerprint is SHA256:…`. Compare it with the fingerprint the operator published; only if they match are you talking to the real server and not an impostor in the middle. SSH remembers it afterwards and refuses to connect, with a warning, if it ever changes.

## iPhone / iPad (iOS)

iOS has no built-in terminal, so you need a third-party SSH client from the App Store and then enter the connection details. Here are the two simplest free ways.

### Option 1: iSH Shell (most direct, you can paste the command)

iSH Shell is a free app that emulates a Linux environment on iOS, so you can run the connection command just like on a computer.

1. Open the App Store, search for **iSH Shell** and install it.
2. The first time, install the SSH client. In the black command-line screen, type this and press Return:

   ```
   apk add openssh-client
   ```

   When it finishes and the `~ #` prompt is back at the bottom, it's installed.
3. Paste the connection command and press Return:

   ```
   ssh -p 2222 guest@term.freedom8964.com
   ```

4. On first connect you'll see `Are you sure you want to continue connecting (yes/no/[fingerprint])?`. Type `yes` and press Return.
5. No password is needed; you go straight into the chat.

### Option 2: Termius or WebSSH (graphical, good for regular use)

To save the address and open it with one tap next time, use a dedicated SSH client such as **Termius** or **WebSSH**. These apps want the command split into fields:

| Field | Value |
| --- | --- |
| Host / Address | `term.freedom8964.com` |
| Port | `2222` (the default is 22 — be sure to change it) |
| Username | `guest` |
| Password | leave empty |

Using Termius as the example:

1. **Install and open the app; no account needed.** Get Termius (or WebSSH) from the App Store. If it asks you to log in or sign up, skip it (Skip / Continue without account); the local features are free.
2. **Add a host.** Tap **+** (New Host) and fill in the table above: Address / Hostname `term.freedom8964.com`; scroll down to the SSH section and change Port from 22 to `2222`; Username `guest`; leave Password empty. Tap Save in the top right; the server appears in the list.
3. **Connect and accept the fingerprint.** Tap the saved host. On first connect a Host Key Verification dialog appears; tap Continue or Accept. When the screen turns into a black terminal showing the chat, bring up the keyboard and start typing.

> Tip: if the phone screen is too narrow, change the username to `plain` for line mode.

## Commands

In the chat, `/help` lists the everyday commands and `/help all` lists everything. Descriptions are shown in Chinese and English.

```
/rooms                 list public rooms
/join <room|code>      join a room or a private room
/create                create a private room and get its code (gone when the last person leaves)
/leave                 back to the lobby
/msg <nick> <text>     private message; /r <text> replies to the last one
/nick <name>           change nickname (2–16 characters, Chinese works)
/who                   who is in the current room
/me <action>           action message
/ignore <nick>         hide someone (/unignore to undo; they won't know)
/report <nick> <why>   report to online moderators
/bell on|off           bell on mentions and private messages
/tz <zone>             show times in your timezone: /tz Europe/Berlin, /tz -5, /tz utc
/theme green|black|white  background: eye-care green (default), black, white (no argument cycles)
/privacy               what this server keeps and doesn't
/forget                erase everything you posted from the room history
/clear  /quit  /help
//text                 send text that starts with /
```

Keys: ↑↓ input history, PgUp/PgDn scroll the chat, Tab completes nicknames and commands, Esc clears the input and jumps to the bottom, Ctrl+A/E/U/W/K line editing, Ctrl+L redraw. Messages that mention you are shown as an orange band starting with `»`; `@nick` inside a message is bold and underlined. Pasting several lines joins them into one, so nothing is sent by accident.

**Language:** every prompt, the help and the key hints are shown in both Simplified Chinese and English. Messages from other people are shown as written, not translated.

**Background and colours (colour-blind and low-vision friendly):** the default is black text on an eye-care green background. Type `/theme` to cycle between green, black and white backgrounds, or pick one with `/theme black`, `/theme white`, `/theme green`. To start with black or white, log in as `black@HOST` or `white@HOST` instead of the usual user name. On all three backgrounds every colour — body text, timestamps, nicknames, hints, errors — is at least 7:1 contrast (WCAG AAA); body text is 19:1 or more. Colours come from the colour-blind-safe Okabe–Ito family and never depend on telling red from green; important things carry a symbol as well as a colour: a `»` band means someone mentioned you, `!` an error, `▸` the current room. Only 256 colours are used, so old terminals and phone SSH apps look the same. While you chat the whole terminal window, padding included, takes the theme colour and goes back to normal when you leave; if the network drops instead, the green may linger — close the tab (or type `reset` on Linux/macOS).

**Font:** your own terminal picks the font; the server can't set it. For a clean sans-serif (黑体) that keeps Chinese and Latin aligned, try the free monospace Sarasa Mono SC. Windows Terminal: Settings → Profiles → Appearance → Font face → `Sarasa Mono SC`, and size 14–16 if you like it larger. macOS Terminal: Settings → Profiles → Text → Font. Phone SSH apps usually have it under Appearance or Terminal.

**Bell:** when someone mentions you or sends you a private message, the terminal rings (phone SSH apps usually vibrate or notify). Don't want it? `/bell off`.

**Join/leave lines in busy rooms:** once a room has more than 20 people, join/leave lines are replaced by one summary every 30 seconds, e.g. "3 joined, 2 left". Kicks and bans are still shown one by one.

**Unread counts across rooms:** the rooms bar at the top shows how many new messages arrived in rooms you're not in, e.g. `#chinese 32 +5`. If someone mentioned you there, it shows an orange-highlighted `@5`, and a line `@ #chinese <Bob> …  · /join chinese` appears in your current room so you can jump over. Chinese nicknames are recognised even without a space after them (`@夜猫子你好`). The count resets when you enter that room. `/rooms` lists the counts too. Mentions inside private rooms don't notify anyone outside, so a room's existence doesn't leak.

## Connecting through Tor (the server never sees your IP)

Server side: `sudo vivant-ctl install --tor` prints the `.onion` address at the end (or see it with `sudo vivant-ctl status`). The onion private key lives in `/var/lib/tor/vivant/`; upgrades and a normal uninstall keep it, only `--purge` deletes it.

User side (needs a local Tor, or Tor Browser running):

```bash
# Linux / macOS (with tor and torsocks installed)
torsocks ssh guest@xxxxxxxx.onion

# without torsocks: through Tor's SOCKS port (9150 for Tor Browser, 9050 for the tor service)
ssh -o ProxyCommand='nc -x 127.0.0.1:9050 %h %p' guest@xxxxxxxx.onion

# Windows (Git for Windows ships connect.exe), with Tor Browser open
ssh -o ProxyCommand="connect -S 127.0.0.1:9150 %h %p" guest@xxxxxxxx.onion
```

The onion address itself authenticates the server, so there's no need to check the SSH host fingerprint separately.
