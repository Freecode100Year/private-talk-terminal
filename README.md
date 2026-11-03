# Private-Talk Terminal · 匿名终端聊天室

一条 SSH 命令进入全球公共聊天室。不用网页、不用注册、不用手机号或邮箱，也不用装专用客户端：Windows CMD / PowerShell、Linux、macOS 自带的 `ssh` 就够了。

```
ssh -p 2222 guest@term.freedom8964.com
```

```
 PRIVATE-TALK TERMINAL                         Alice · Online 3 · Rooms 5 · Up 4d 12h
#general  3 here  ·  大厅 · Lobby — 什么都可以聊
┌─ ROOMS ──────────────┐│[18:32] <Alice> 大家晚上好！
│ #general            3││[18:33] <Ghost> Hello from New York!
│ #chinese            0││[18:33] <Anon42> 有人在学编程吗？
│ #english            0││[18:34] [私信 from Ghost] hi
│ #technology         0││[18:34] -- User_829 joined.
│ #anonymous          0││
└──────────────────────┘│
─────────────────────────────────────────────────────────────────────────────────────
> 说点什么… type a message or /help
Enter 发送 · ↑↓ 历史 · PgUp/PgDn 滚动 · Tab 补全 · Esc 清空 · Ctrl+C 退出  ● CONNECTED
```

纯 Go、单个静态二进制、无数据库、所有数据只在内存里。

## 用户怎么连接

| 方式 | 命令 |
| --- | --- |
| 完整界面（推荐） | `ssh -p 2222 guest@HOST` |
| 纯文本模式（老终端、CMD 显示乱码时） | `ssh -p 2222 plain@HOST` |
| 不分配终端 / 脚本 | `ssh -T -p 2222 guest@HOST` |
| 最大匿名（不让客户端发送你的公钥） | `ssh -o PubkeyAuthentication=no -p 2222 guest@HOST` |

- 用户名随便填（`guest`、`plain` 有特殊含义）；没有密码。如果客户端问密码，直接回车。
- 首次连接会提示确认服务器主机密钥，输入 `yes`。
- Windows 10/11 自带 OpenSSH 客户端；没有的话在“设置 → 可选功能”里添加。中文显示优先用 Windows Terminal；老 CMD 乱码时用 `plain@`，或先执行 `chcp 65001`。

### 命令

```
/rooms                 列出公共频道
/join <room|code>      进入频道或私人房间（/join chinese）
/create                创建私人房间，得到房间码，凭码进入
/leave                 回到 #general
/msg <nick> <text>     私聊；/r <text> 回复上一个私信你的人
/nick <name>           修改昵称（2–16 字，中文可以）
/who                   当前房间在线用户
/me <action>           动作消息
/clear  /quit  /help
//text                 发送以 / 开头的普通文字
```

## 安全与匿名性（请如实告诉用户）

- **SSH 只加密传输，不是端到端加密。** 服务器进程能读到消息内容。界面里每次进入都会提醒这一点。
- **不提供 Shell。** 没有 exec、SFTP/SCP、端口转发、代理转发、X11；连上后唯一能做的就是聊天（有集成测试覆盖）。
- **不记录 IP。** 日志里没有 IP、昵称和消息。限流和封禁用的是“IP 的 HMAC”，密钥只在内存里、每次重启都变。IPv6 按 /64 归并，防止换地址绕过。
- **不收集公钥。** 只有在管理员名单里的公钥会被接受；其他人走空的 keyboard-interactive 登录。注意 SSH 协议本身会让客户端先“出示”公钥，想彻底避免就加 `-o PubkeyAuthentication=no`。
- **消息不落盘。** 每个房间只在内存保留最近 50 条、最长 1 小时（可配置）。私人房间最后一个人离开即销毁，连同历史。私聊不保存。
- **终端注入防护。** 所有昵称和消息都会去掉控制字符、ANSI/OSC 转义序列和 Unicode 双向覆盖字符，别人无法改你的终端标题、清你的屏或伪造文字方向。
- 防滥用：每个地址最多 3 个会话、8 个 TCP 连接、每分钟 10 次新连接；消息令牌桶（默认突发 5 条、每秒 1 条）；连续重复拦截；频繁超限自动禁言 2 分钟；空闲 30 分钟断开。
- VPS 本身（以及你的云服务商）仍然能看到连接来源 IP，这一点无法在应用层消除。

## 部署到 VPS（Debian 13）

### 1. 拿到二进制

直接用 `dist/` 里的静态二进制（`ptterm-linux-amd64` 或 Oracle Ampere 等 ARM 机器用 `ptterm-linux-arm64`），或自己编译：

```bash
# 需要 Go 1.26.8+
make dist          # 首次会自动 go mod tidy 生成 go.sum
```

### 2. 安装为 systemd 服务

```bash
sudo install -m 0755 ptterm-linux-amd64 /usr/local/bin/ptterm
sudo install -m 0644 deploy/ptterm.service /etc/systemd/system/ptterm.service
sudo install -m 0644 deploy/ptterm.env.example /etc/ptterm.env   # 按需修改
sudo systemctl daemon-reload
sudo systemctl enable --now ptterm
journalctl -u ptterm -f
```

服务用 `DynamicUser` 运行在一次性的无权限账户下，开了完整的 systemd 沙箱（只读文件系统、无 capability、系统调用过滤）。主机密钥第一次启动时自动生成在 `/var/lib/ptterm/host_ed25519`，**请备份它**：换了密钥，老用户会看到“主机密钥变更”警告。

### 3. 防火墙

保留 22 给你自己管理 VPS，2222 给聊天室：

```bash
sudo nft add rule inet filter input tcp dport 2222 accept   # nftables
# 或：sudo ufw allow 2222/tcp
```

Oracle Cloud 还要在 VCN 的 Security List / NSG 里放行 TCP 2222 入站，并检查镜像自带的 iptables 规则。

### 4. 域名

给 `term.freedom8964.com` 加 A / AAAA 记录指向 VPS。**在 Cloudflare 上必须设为 DNS only（灰云）**，橙云代理只转发 HTTP(S)，SSH 会连不上。

### 5. 管理员

```bash
sudo mkdir -p /etc/ptterm
sudo cp ~/.ssh/id_ed25519.pub /etc/ptterm/admins.pub && sudo chmod 0644 /etc/ptterm/admins.pub
echo 'PTT_ADMIN_KEYS=/etc/ptterm/admins.pub' | sudo tee -a /etc/ptterm.env
sudo systemctl restart ptterm
```

用对应私钥连接（`ssh -i ~/.ssh/id_ed25519 -p 2222 guest@HOST`），状态栏会显示 `[ADMIN]`，并多出命令：

```
/kick <nick> [reason]   /mute <nick> [分钟]   /unmute <nick>
/ban <nick> [分钟]      /unbanall   /broadcast <text>   /topic <text>
```

封禁只在内存中，重启后清空。

### 想用 22 端口？

先把 VPS 的 sshd 挪到别的端口，再把 `PTT_ADDR=:22` 写进 `/etc/ptterm.env`，并在 service 里加 `AmbientCapabilities=CAP_NET_BIND_SERVICE` 和 `CapabilityBoundingSet=CAP_NET_BIND_SERVICE`。用户就可以直接 `ssh term.freedom8964.com`。

## 配置参数

| 参数 | 环境变量 | 默认 |
| --- | --- | --- |
| `-addr` | `PTT_ADDR` | `:2222` |
| `-host-key` | `PTT_HOST_KEY` | `./data/host_ed25519` |
| `-admin-keys` | `PTT_ADMIN_KEYS` | 无 |
| `-rooms` | `PTT_ROOMS` | general, chinese, english, technology, anonymous |
| `-history` / `-history-ttl` | | 50 条 / 1h |
| `-max-clients` / `-max-per-ip` / `-conn-per-min` | | 1000 / 3 / 10 |
| `-msg-rate` / `-msg-burst` / `-max-len` | | 1/s / 5 / 500 字 |
| `-idle-timeout` | | 30m |

`-rooms` 格式：`name:主题,name2:主题2`，第一个是大厅。

## 项目结构

```
main.go                     参数、启动、优雅退出
internal/chat/              房间、身份、私聊、历史、限流、封禁（与 SSH 无关，可单测）
internal/ui/tui.go          Bubble Tea v2 完整界面
internal/ui/plain.go        纯文本行模式（服务器端回显 + 输入行重绘）
internal/server/            Wish v2 SSH 网关、认证、连接限制
deploy/                     systemd 单元和配置示例
```

技术栈：Go 1.26 · [Wish v2](https://github.com/charmbracelet/wish) · [Bubble Tea v2](https://github.com/charmbracelet/bubbletea) · Lip Gloss v2 · Bubbles v2。

```bash
make test    # 单元测试 + 真实 SSH 端到端测试（含 exec/转发被拒）
make run     # 本地 127.0.0.1:2222
```

## 路线图

- [x] 第一阶段：SSH 无账号登录、随机昵称、公共频道、实时消息、正常退出
- [x] 第二阶段：房间列表、私人房间码、私聊、中文、在线人数、断线清理
- [x] 第三阶段（基础）：限流、管理员工具、消息过期、权限隔离、systemd 沙箱
- [ ] 第三阶段（剩余）：负载测试、举报命令、封禁持久化（可选）
- [ ] 第四阶段：与 Private-Talk 打通。注意：普通 SSH 客户端无法做到端到端加密，要让服务器读不到内容，必须在用户本地有专用加密客户端。
