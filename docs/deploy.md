# 部署与配置

[← 返回 README](../README.md) · 简体中文 | [English](deploy.en.md)

## 一键安装

在 VPS 上（Debian / Ubuntu / RHEL 系等任何 systemd Linux，x86_64、ARM64、ARMv7）：

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh | sudo bash
```

带参数（注意 `-s --`）：

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh \
  | sudo bash -s -- --port 2222 --admin-key github:你的GitHub用户名 --domain term.freedom8964.com
```

脚本会自动完成以下步骤：

1. 识别 CPU 架构，从 GitHub Release 下载对应二进制并**校验 SHA256**（下载失败时自动从源码编译）
2. 安装为带完整沙箱的 systemd 服务（一次性无权限用户、只读系统、无 capability）
3. 生成 `/etc/vivant.env` 配置，按机器内存设置 `GOMEMLIMIT`
4. 放行防火墙端口（ufw / firewalld / iptables，包括 Oracle Cloud 镜像自带的 REJECT 规则）
5. 检查端口占用，启动服务并确认在监听，最后打印连接命令和主机密钥指纹
6. 安装管理命令 `vivant-ctl`

之后的管理：

```bash
sudo vivant-ctl status            # 运行状态 + 在线人数
sudo vivant-ctl doctor            # 自检：服务、端口、防火墙、域名解析、Cloudflare 灰云、主机密钥、新版本
sudo vivant-ctl upgrade           # 升级到最新版，保留配置和主机密钥
sudo vivant-ctl install --port 2299 --admin-key ~/.ssh/id_ed25519.pub   # 改配置（可重复执行）
sudo vivant-ctl uninstall         # 卸载（保留配置和主机密钥）
sudo vivant-ctl uninstall --purge # 彻底删除
```

安装脚本的所有输出都是中英双语。

| 安装参数 | 说明 |
| --- | --- |
| `--port N` | 聊天室 SSH 端口，默认 2222。用 22 需先把系统 sshd 挪走，脚本会自动加端口权限 |
| `--admin-key KEY` | 管理员公钥：`ssh-ed25519 AAAA…`、`.pub` 文件路径，或 `github:用户名`；可重复 |
| `--rooms LIST` | 公共频道，如 `"general:大厅,chinese:中文,tech:技术"`（第一个是大厅） |
| `--domain NAME` | 用户连接用的域名：显示在最后的连接命令里，`doctor` 也用它检查解析 |
| `--version vX.Y.Z` | 安装指定版本 |
| `--alt-port N` | 同时监听第二个端口，例如 `443`：公司、学校、酒店网络常只放行 80/443。默认不开。443 这类知名端口会一直被扫描器探测，日志和连接数会多不少噪音（扫描连接 30 秒内没进入聊天就会被断开，同样受每地址限额约束）；用户用不上就别开。机器上已有网站占用 443 时用不了，安装器会检查并提示；`--alt-port off` 关闭 |
| `--tor` | 同时发布为 Tor 洋葱服务（自动安装 Tor），用户可以不暴露 IP 连接，见[安全与匿名性](security.md) |
| `--persist-bans` | 封禁在重启和升级后依然有效（默认只存在内存里）；`--no-persist-bans` 关闭。隐私上的取舍见[安全与匿名性](security.md) |
| `--from-source` | 从源码编译（会临时下载 Go，结束后删除） |
| `--no-firewall` | 不改防火墙 |

网络受限时：`VIVANT_DOWNLOAD_BASE=<镜像地址>` 指向放有 `vivant-linux-<arch>` 和 `SHA256SUMS` 的目录；源码编译可设 `GOPROXY=https://goproxy.cn`。

**还需要你手动做的两件事：**

- 云厂商的防火墙（Oracle VCN 安全列表、AWS/GCP 安全组等）放行 TCP 端口。
- 域名 A/AAAA 记录指向 VPS。在 Cloudflare 上必须设为 **DNS only（灰云）**，橙云代理不转发 SSH。

## 离线安装（服务器连不上 GitHub）

每次发布都附带一个离线一键安装包 `vivant-<版本>-linux-offline.sh`：一个文件里包含三种架构的程序、安装脚本和校验文件，运行时不联网。在能上网的电脑上从 [Releases](https://github.com/Freecode100Year/vivant/releases) 下载，然后任选一种方式：

```bash
# 方式一：在自己电脑上一条命令部署到服务器（Linux、macOS，或 Windows 的 Git Bash / WSL）
bash vivant-v0.10.0-linux-offline.sh --to root@服务器

# 方式二：先把文件传到服务器，再在服务器上运行
scp vivant-v0.10.0-linux-offline.sh root@服务器:
sudo bash vivant-v0.10.0-linux-offline.sh
```

后面可以跟和在线安装一样的参数，例如 `--to root@服务器 --port 2222 --admin-key ~/.ssh/id_ed25519.pub`（方式一里公钥文件路径指的是服务器上的路径，离线时请先把公钥传上去，或用方式二）。`--to` 用的是你电脑上的 ssh/scp，服务器上的用户不是 root 时会自动用 sudo。

安装包会先校验自身（下载不完整或被改动会直接拒绝），再解压出安装脚本和程序，安装脚本按 `SHA256SUMS` 再校验一次程序后安装，结束后删除临时文件。升级时下载新的离线包再运行一次即可，配置和主机密钥会保留。`bash vivant-…-linux-offline.sh --extract` 只解压不安装，`--help` 列出全部参数。

仍然需要联网的只有：`--tor`（要从软件源装 tor，已装好则不用）和 `--admin-key github:用户名`（离线时请直接给公钥文件）。离线服务器上的 `vivant-ctl upgrade` 会因为连不上 GitHub 而失败，升级请用新的离线包。

## Windows / macOS

服务器建议用 Linux。想在 Windows 或 Mac 上临时跑一个（本机测试、局域网里和朋友聊），发布页里有 `vivant-windows-amd64.exe`、`vivant-windows-arm64.exe`、`vivant-macos-amd64`、`vivant-macos-arm64`，下载后直接运行：

```bash
./vivant-macos-arm64 -addr :2222          # macOS（Apple 芯片）；首次运行需在「系统设置 → 隐私与安全性」里允许
vivant-windows-amd64.exe -addr :2222      # Windows；防火墙弹窗时允许专用网络
```

然后其他人用 `ssh -p 2222 guest@你的局域网IP` 连接。这种方式没有安装器、没有 systemd 沙箱、不会开机自启，主机密钥保存在当前目录的 `data/` 里，只适合测试和局域网，不建议对公网开放。

## 从 Private-Talk Terminal 迁移

VIVANT 以前叫 Private-Talk Terminal（程序名 `ptterm`）。已经部署了旧版的服务器，运行一次上面的一键安装命令即可自动迁移：

- 主机密钥、配置（`PTT_*` 改为 `VIVANT_*`）、管理员公钥、封禁、Tor 洋葱地址全部保留，用户不会看到“主机密钥已变更”警告，洋葱地址也不变。
- 服务名改为 `vivant`，程序改为 `/usr/local/bin/vivant`，配置文件改为 `/etc/vivant.env`，数据目录改为 `/var/lib/vivant`。
- 旧命令 `ptterm-ctl` 会保留为指向 `vivant-ctl` 的快捷方式。

还没迁移的服务器执行 `sudo ptterm-ctl upgrade` 也能升级到新版本程序（继续沿用旧名字），之后随时再运行一键安装命令完成迁移。

## 网页入口（浏览器 + WebSocket）

给装不了 SSH、或网络只放行 HTTPS 的人（以及只能发 HTTPS 请求的程序、AI 助手）一条路。网页用户和 SSH 用户在同一个聊天室里，界面是纯文本模式，命令完全一样。

```bash
sudo vivant-ctl install --web-port 8080                     # 前面是 Cloudflare Tunnel（默认）
sudo vivant-ctl install --web-port 8080 --web-proxy proxy   # 前面是 Caddy / nginx
sudo vivant-ctl install --web-port off                      # 关闭
```

网页入口只监听 `127.0.0.1`，不开防火墙端口，必须用一个 HTTPS 前端把它接出去。**SSH 用的域名在 Cloudflare 上必须是灰云，所以网页入口要用另一个子域名**，例如 `chat.freedom8964.com`。

**方式一：Cloudflare Tunnel（推荐，不用开端口、不用证书）**

```bash
# 在服务器上装好 cloudflared 并登录：cloudflared tunnel login
cloudflared tunnel create vivant-web
cloudflared tunnel route dns vivant-web chat.freedom8964.com
cat > ~/.cloudflared/config.yml <<'CFG'
tunnel: vivant-web
credentials-file: /root/.cloudflared/<隧道ID>.json
ingress:
  - hostname: chat.freedom8964.com
    service: http://127.0.0.1:8080
  - service: http_status:404
CFG
sudo cloudflared service install
```

**方式二：Caddy（自动申请证书）**，配合 `--web-proxy proxy`：

```caddyfile
chat.freedom8964.com {
    reverse_proxy 127.0.0.1:8080
}
```

nginx 需要自己加 `proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection "upgrade";` 和 `X-Forwarded-For`。

`--web-proxy` 决定从哪个请求头读真实地址（用于每地址限额和封禁，地址只会变成哈希，不记录）：`cloudflare` 读 `CF-Connecting-IP`，`proxy` 读 `X-Forwarded-For` 最后一项。选错会让所有网页用户共用一个限额，或让人伪造地址绕过限额。

**隐私取舍，请告知用户：** 走网页时，HTTPS 在 Cloudflare 或你的反代上解密，它们能看到用户 IP 和聊天内容；浏览器靠 CA 证书而不是 SSH 主机指纹认服务器。追求匿名的人仍然推荐 SSH 或 Tor。页面本身不加载任何第三方资源、不设 Cookie、不用本地存储，并带严格的 CSP。

**给程序用（`/ws` 协议）：** 连接 `wss://chat.example.com/ws`。服务器发来的每条文本消息是一段纯文本输出（用 `\n` 分行，偶尔有 `\a` 响铃）；客户端发出的每条文本消息就是一行输入（聊天内容或 `/命令`），和 SSH 里打字一样。带 `Origin` 头的浏览器请求只允许同源（`-web-origins` 可加白名单），不带 `Origin` 的脚本不受限制。Python 示例：

```python
import websocket                       # pip install websocket-client
ws = websocket.create_connection("wss://chat.freedom8964.com/ws")
ws.send("/nick bot")
ws.send("大家好")
print(ws.recv())
```

## 连不上？先跑自检

```bash
sudo vivant-ctl doctor --domain chat.example.com
```

逐项检查并给出修复命令，有问题时退出码为 1：

- 服务是否在运行、是否开机自启、是否反复重启
- 聊天端口（和备用端口）是否在监听、有没有被别的程序占用、本机防火墙是否放行
- 域名是否解析到本机；如果解析到 Cloudflare 的代理地址（橙色云朵），会提示改成“仅 DNS”（灰色云朵），因为 SSH 过不了 Cloudflare 代理
- Tor（如果开了）：tor 是否运行、洋葱地址、本地监听
- 主机密钥是否存在、权限是否 600，打印指纹和备份命令
- 有没有新版本

`--domain` 只需给一次：安装时用过 `--domain` 也会被记住。云服务商的安全组（Oracle VCN、AWS/GCP 安全组等）在服务器内部看不到，需要从别的网络实际连一次。

## 外部监控

想在聊天室挂掉时收到通知，用任意外部监控服务（UptimeRobot、Uptime Kuma、Better Stack 等）建一个 **TCP 端口** 监控，目标 `你的域名:2222`，间隔 1–5 分钟即可。只探测端口通不通，不登录、不进聊天室，也不需要在服务器上开放任何额外接口。

健康检查接口 `127.0.0.1:2223/healthz` 只监听本机，不建议为了监控把它暴露到公网；需要在线人数曲线的话，在服务器本机用 cron 或 node_exporter 之类的工具读取。

## 公布主机指纹

用户第一次连接时，SSH 会显示服务器的主机密钥指纹。请把指纹公布在用户能看到的地方（例如 README、网站），用户核对一致才能确认没有被冒充：

```bash
sudo vivant-ctl status        # 输出里的 Host key: SHA256:…
```

主机密钥在 `/var/lib/vivant/host_ed25519`，务必备份。换了密钥，指纹就会变，老用户会看到警告。

## 管理员

用户名填 `admin`，并用安装时登记的私钥连接（`ssh -p 2222 -i ~/.ssh/id_ed25519 admin@HOST`），分隔线上会显示 `[ADMIN]`。只有 `admin` 这个用户名才会检查密钥：管理员的密钥配错了会直接被拒绝，不会悄悄降级成普通用户。

```
/kick <nick> [reason]   /mute <nick> [分钟]   /unmute <nick>
/ban <nick> [分钟]      /bans   /unban <id>   /unbanall
/broadcast <text>       /topic <text>   /reports
```

封禁对象是“地址”的匿名哈希（见[安全与匿名性](security.md)）。`/bans` 列出封禁的哈希前缀和剩余时间，`/unban <id>` 解除。默认只存在内存里、重启后清空；安装时加 `--persist-bans` 可以让封禁跨重启保留。

## 配置

配置文件 `/etc/vivant.env`，改完执行 `systemctl restart vivant`：

| 环境变量 / 参数 | 默认 | 说明 |
| --- | --- | --- |
| `VIVANT_ADDR` / `-addr` | `:2222` | 监听地址 |
| `VIVANT_ADMIN_KEYS` / `-admin-keys` | — | 管理员公钥文件 |
| `VIVANT_ROOMS` / `-rooms` | 5 个默认频道 | `name:主题,name2:主题2` |
| `VIVANT_MOTD` / `-motd` | — | 进入时额外显示的公告文本文件 |
| `VIVANT_HEALTH_ADDR` / `-health-addr` | `127.0.0.1:2223` | `GET /healthz`，只返回计数 |
| `VIVANT_ADMIN_USER` / `-admin-user` | `admin` | 管理员登录用的用户名 |
| `VIVANT_ONION_ADDR` / `-onion-addr` | — | 给 Tor 洋葱服务用的本地监听地址（`--tor` 自动设为 `127.0.0.1:2224`） |
| `VIVANT_BAN_FILE` / `-ban-file` | — | 封禁持久化文件（`--persist-bans` 设为 `/var/lib/vivant/bans.json`） |
| `VIVANT_ALT_ADDR` / `-alt-addr` | — | 额外监听地址，逗号分隔（`--alt-port 443` 设为 `:443`） |
| `VIVANT_WEB_ADDR` / `-web-addr` | — | 网页入口监听地址（`--web-port 8080` 设为 `127.0.0.1:8080`），见[网页入口](#网页入口浏览器--websocket) |
| `VIVANT_WEB_PROXY_HEADER` / `-web-proxy-header` | — | 从哪个请求头取真实地址（`--web-proxy` 设置）；直连时不要设 |
| `VIVANT_WEB_ORIGINS` / `-web-origins` | — | 额外允许打开 `/ws` 的网页来源，逗号分隔 |
| `VIVANT_TOR_EXITS` / `-tor-exits` | `on` | 定期下载 Tor 出口节点列表，让 Tor 用户共用宽松限额；`off` 关闭 |
| `-quiet-above` | 20 | 房间超过这个人数后，进出提示改为每 30 秒汇总一次（-1 = 不汇总） |
| `-history` / `-history-ttl` | 50 / 1h | 每房间历史条数和保留时长 |
| `-max-clients` / `-max-per-ip` / `-max-tcp-per-ip` / `-conn-per-min` | 1000 / 3 / 8 / 10 | 连接限制 |
| `-read-rate` | 32768 | 每个连接每秒最多发来多少字节，超出会被放慢而不是断开（突发 8 倍；0 = 不限）。正常打字和粘贴远远用不到 |
| `-msg-rate` / `-msg-burst` / `-max-len` | 1/s / 5 / 500 | 发言限制 |
| `-idle-timeout` | 1h | 不发言多久后断开（0 = 不断开） |
| `GOMEMLIMIT` | 内存的 60% | Go 运行时软内存上限 |

其它参数写进 `VIVANT_ARGS=`。全部参数：`vivant -h`。

**主机密钥** 在 `/var/lib/vivant/host_ed25519`，请备份。换了密钥，老用户会看到“主机密钥已变更”警告。

## Docker（可选）

一键脚本（systemd）仍是推荐方式。习惯用容器的话，每次发布都会推送多架构镜像（amd64 / arm64 / armv7）到 GHCR：

```bash
docker run -d --name vivant --restart unless-stopped --network host \
  --read-only --cap-drop ALL --security-opt no-new-privileges --ulimit core=0 \
  -v vivant-data:/data -e GOMEMLIMIT=600MiB \
  ghcr.io/freecode100year/vivant:latest
```

或者用仓库里的 [`deploy/docker-compose.yml`](../deploy/docker-compose.yml)：`docker compose -f deploy/docker-compose.yml up -d`。

- **一定要用 `--network host`。** 用 `-p 2222:2222` 时，Docker 的端口代理（IPv6、某些发行版的 IPv4）会把所有用户的来源都变成同一个地址，大家就共用一份限流额度，一个人刷屏会连累所有人，封禁也会误伤所有人。
- 镜像基于 distroless，没有 shell，以非 root 用户（uid 65532）运行；主机密钥保存在卷 `vivant-data` 的 `/data/host_ed25519`，删卷等于换密钥。
- 配置用环境变量（同上表的 `VIVANT_*`）或在镜像名后追加参数，例如 `… vivant:latest -max-per-ip 3`。
- 防火墙、Tor、`vivant-ctl doctor` 这些由一键脚本处理的事情，容器部署需要自己做。
- 自己构建：`docker build -t vivant .`
