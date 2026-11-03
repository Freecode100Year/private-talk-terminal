# Private-Talk Terminal · 匿名终端聊天室

[![CI](https://github.com/Freecode100Year/private-talk-terminal/actions/workflows/ci.yml/badge.svg)](https://github.com/Freecode100Year/private-talk-terminal/actions/workflows/ci.yml)

一条 SSH 命令进入全球公共聊天室。不用网页、不用注册、不用手机号或邮箱，也不用装专用客户端：Windows CMD / PowerShell、Linux、macOS 自带的 `ssh` 就够了。

```
ssh -p 2222 guest@term.freedom8964.com
```

```
 PRIVATE-TALK TERMINAL                          Alice · Online 3 · Up 4d 12h · 18:34 CST
ROOMS ▸#general 3   #chinese 0   #english 0   #technology 0   #anonymous 0
[18:32] <Alice> 大家晚上好！
[18:33] <Ghost> Hello from New York!
[18:33] <Anon42> 有人在学编程吗？@Alice
[18:34] [私信 from Ghost] hi
[18:34] -- User_829 joined.
── #general · 3 here · 大厅 · Lobby — 什么都可以聊 ──────────────────────────────────
> 说点什么… type a message or /help
Enter 发送 · ↑↓ 历史 · PgUp/PgDn 滚动 · Tab 补全 · Esc 清空 · Ctrl+C 退出   ● CONNECTED
```

纯 Go 单个静态二进制，无数据库，所有数据只在内存里。

## 在线实例

Private-Talk Terminal `v0.2.0` 已部署并可从公网连接：

```bash
ssh -p 2222 guest@term.freedom8964.com
```

---

## 一键部署

在 VPS 上（Debian / Ubuntu / RHEL 系等任何 systemd Linux，x86_64、ARM64、ARMv7）：

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/private-talk-terminal/main/install.sh | sudo bash
```

带参数（注意 `-s --`）：

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/private-talk-terminal/main/install.sh \
  | sudo bash -s -- --port 2222 --admin-key github:你的GitHub用户名 --domain term.freedom8964.com
```

脚本会自动完成以下步骤：

1. 识别 CPU 架构，从 GitHub Release 下载对应二进制并**校验 SHA256**（下载失败时自动从源码编译）
2. 安装为带完整沙箱的 systemd 服务（一次性无权限用户、只读系统、无 capability）
3. 生成 `/etc/ptterm.env` 配置，按机器内存设置 `GOMEMLIMIT`
4. 放行防火墙端口（ufw / firewalld / iptables，包括 Oracle Cloud 镜像自带的 REJECT 规则）
5. 检查端口占用，启动服务并确认在监听，最后打印连接命令和主机密钥指纹
6. 安装管理命令 `ptterm-ctl`

之后的管理：

```bash
sudo ptterm-ctl status            # 运行状态 + 在线人数
sudo ptterm-ctl upgrade           # 升级到最新版，保留配置和主机密钥
sudo ptterm-ctl install --port 2299 --admin-key ~/.ssh/id_ed25519.pub   # 改配置（可重复执行）
sudo ptterm-ctl uninstall         # 卸载（保留配置和主机密钥）
sudo ptterm-ctl uninstall --purge # 彻底删除
```

| 安装参数 | 说明 |
| --- | --- |
| `--port N` | 聊天室 SSH 端口，默认 2222。用 22 需先把系统 sshd 挪走，脚本会自动加端口权限 |
| `--admin-key KEY` | 管理员公钥：`ssh-ed25519 AAAA…`、`.pub` 文件路径，或 `github:用户名`；可重复 |
| `--rooms LIST` | 公共频道，如 `"general:大厅,chinese:中文,tech:技术"`（第一个是大厅） |
| `--domain NAME` | 只用于最后显示的连接命令 |
| `--version vX.Y.Z` | 安装指定版本 |
| `--tor` | 同时发布为 Tor 洋葱服务（自动安装 Tor），用户可以不暴露 IP 连接，见下文 |
| `--from-source` | 从源码编译（会临时下载 Go，结束后删除） |
| `--no-firewall` | 不改防火墙 |

网络受限时：`PTT_DOWNLOAD_BASE=<镜像地址>` 指向放有 `ptterm-linux-<arch>` 和 `SHA256SUMS` 的目录；源码编译可设 `GOPROXY=https://goproxy.cn`。

**还需要你手动做的两件事：**

- 云厂商的防火墙（Oracle VCN 安全列表、AWS/GCP 安全组等）放行 TCP 端口。
- 域名 A/AAAA 记录指向 VPS。在 Cloudflare 上必须设为 **DNS only（灰云）**，橙云代理不转发 SSH。

---

## 用户怎么连接

| 方式 | 命令 |
| --- | --- |
| 完整界面（推荐） | `ssh -o StrictHostKeyChecking=accept-new -p 2222 guest@HOST` |
| 纯文本模式（老终端 / CMD 乱码时） | `ssh -o StrictHostKeyChecking=accept-new -p 2222 plain@HOST` |
| 无终端 / 脚本 / 机器人 | `ssh -o StrictHostKeyChecking=accept-new -T -p 2222 guest@HOST` |
| 通过 Tor（服务器看不到你的 IP） | `torsocks ssh guest@<onion地址>.onion`（见下文“通过 Tor 连接”） |

- 用户名随便填（`admin` 留给管理员），没有密码，连上直接进聊天室。
- 使用上面的 `accept-new` 选项后，首次连接会自动记录服务器主机密钥；之后若密钥变化，SSH 会拒绝连接并提示风险。
- 客户端不会向服务器发送你的公钥，不需要额外加 `PubkeyAuthentication=no`（原理见“安全与匿名性”）。
- Windows 10/11 自带 OpenSSH；中文显示推荐 Windows Terminal。老 CMD 乱码时用 `plain@`，或先执行 `chcp 65001`。

如果希望命令中不写 `-o StrictHostKeyChecking=accept-new`，在 SSH 配置文件中为该域名设置一次：Windows 文件为 `%USERPROFILE%\.ssh\config`，macOS/Linux 文件为 `~/.ssh/config`。把下面内容加到文件靠前位置：

```sshconfig
Host term.freedom8964.com
    StrictHostKeyChecking accept-new
```

之后可直接运行 `ssh -p 2222 guest@term.freedom8964.com`。首次自动保存密钥，后续仍会拒绝密钥变化。

### iPhone / iPad（iOS）

iOS 没有内置的终端工具，需要从 App Store 下载一款第三方 SSH 客户端，再把连接命令里的参数填进去。下面是两种最简单的免费方法。

#### 方法一：iSH Shell（最直接，可以直接粘贴命令）

iSH Shell 是一个在 iOS 上模拟 Linux 环境的免费应用，可以像在电脑上一样直接运行连接命令。

1. 打开 App Store，搜索并下载 **iSH Shell**。
2. 首次打开时先安装 SSH 客户端。在黑色命令行界面输入下面的命令并按回车：

   ```
   apk add openssh-client
   ```

   等进度走完、底部再次出现 `~ #` 提示符，就表示安装成功。
3. 复制并粘贴连接命令，然后按回车：

   ```
   ssh -p 2222 guest@term.freedom8964.com
   ```

4. 首次连接时屏幕会提示 `Are you sure you want to continue connecting (yes/no/[fingerprint])?`，输入 `yes` 并按回车。
5. 不需要密码，确认后就会直接进入聊天室界面。

#### 方法二：Termius 或 WebSSH（图形界面，适合经常连接）

如果想保存这个地址、下次一键打开，可以用专门的 SSH 客户端（例如 **Termius** 或 **WebSSH**）。这类应用需要把命令拆开填写：

| 参数 | 填写内容 |
| --- | --- |
| 主机地址（Host / Address） | `term.freedom8964.com` |
| 端口（Port） | `2222`（默认是 22，务必改成 2222） |
| 用户名（Username） | `guest` |
| 密码（Password） | 留空 |

以 Termius 为例：

1. **下载并打开客户端，不用注册。** 在 App Store 下载 Termius（或 WebSSH）。打开后如果提示登录或注册，选择跳过（Skip / Continue without account），本地功能即可免费使用。
2. **新建主机。** 点界面上的 **+** 号（New Host），按上表填写：Address / Hostname 填 `term.freedom8964.com`；往下滑动找到 SSH 配置栏，把 Port 从 22 改成 `2222`；Username 填 `guest`；Password 留空。点右上角的保存（Save），列表里会出现刚创建的服务器图标。
3. **连接并确认指纹。** 点保存好的主机图标发起连接。首次连接会弹出主机密钥确认窗口（Host Key Verification），点 Continue 或 Accept。屏幕切换成黑色终端界面并显示出聊天室文字后，呼出键盘就可以开始打字了。

> 小提示：手机屏幕窄、显示效果不理想时，可以把用户名改成 `plain`，进入纯文本模式。

### 命令

```
/rooms                 列出公共频道
/join <room|code>      进入频道或私人房间
/create                创建私人房间，得到房间码，凭码进入（最后一人离开即销毁）
/leave                 回到大厅
/msg <nick> <text>     私聊；/r <text> 回复上一个私信你的人
/nick <name>           修改昵称（2–16 字，中文可以）
/who                   当前房间在线用户
/me <action>           动作消息
/ignore <nick>         屏蔽某人（/unignore 取消；对方不会知道）
/report <nick> <why>   举报给在线管理员
/tz <zone>             按你的时区显示时间：/tz Asia/Shanghai、/tz +8、/tz utc
/clear  /quit  /help
//text                 发送以 / 开头的普通文字
```

按键：↑↓ 输入历史，PgUp/PgDn 翻看聊天记录，Tab 补全昵称和命令，Esc 清空输入并回到底部，Ctrl+A/E/U/W/K 行编辑，Ctrl+L 重绘。别人 @ 你的消息会高亮。粘贴多行文字会合并成一行，不会误发。

### 管理员

用户名填 `admin`，并用安装时登记的私钥连接（`ssh -p 2222 -i ~/.ssh/id_ed25519 admin@HOST`），分隔线上会显示 `[ADMIN]`。只有 `admin` 这个用户名才会检查密钥：管理员的密钥配错了会直接被拒绝，不会悄悄降级成普通用户。

```
/kick <nick> [reason]   /mute <nick> [分钟]   /unmute <nick>
/ban <nick> [分钟]      /unbanall   /broadcast <text>   /topic <text>   /reports
```

封禁对象是“地址”（见下文），只在内存中，重启后清空。

---

## 安全与匿名性（请如实告诉用户）

- **SSH 只加密传输，不是端到端加密。** 服务器进程能读到消息内容，每次进入都会提醒。
- **不提供 Shell。** 没有 exec、SFTP/SCP、端口转发、代理转发、X11。CI 会在真实服务器上验证这一点。
- **不记录 IP。** 日志里没有 IP、昵称或消息，第三方库写的日志也会统一过滤掉地址字段。限流和封禁使用“IP 的 HMAC”，密钥只在内存里，每次重启都换；IPv6 按 /64 归并。健康检查接口只返回计数。
- **客户端根本不会发送公钥。** 正常情况下，SSH 客户端会把你电脑上的公钥逐个“出示”给服务器。GitHub 上每个人的公钥都是公开的，服务器拿来一比对就能认出你是谁。本服务在 SSH 认证的第一步（`none`）就放行普通用户，客户端还没走到出示公钥那一步。只有 `admin` 用户名才会进入密钥认证。可以用 `ssh -v` 验证：输出里是 `Authenticated ... using "none"`，没有 `Offering public key`。CI 里也有对应的测试。
- **消息不落盘。** 每个房间只在内存保留最近 50 条、最长 1 小时；私人房间清空即销毁；私聊不保存。
- **终端注入防护。** 昵称和消息里的控制字符、ANSI/OSC 转义序列、Unicode 双向覆盖字符一律剔除。
- **防滥用。** 每个地址最多 3 个会话、8 个 TCP 连接、每分钟 10 次新连接；消息令牌桶（突发 5 条、每秒 1 条）；重复消息拦截；频繁超限自动禁言；1 小时不发言自动断开。
- **Tor 洋葱服务（可选）。** 普通连接下，VPS 和云服务商仍然能看到来源 IP，这在应用层无法消除。安装时加 `--tor`，聊天室会同时发布为 `.onion` 地址：通过 Tor 进来的用户，服务器只能看到“来自 Tor”，看不到真实 IP。代价是这些用户无法按地址封禁（`/ban` 会改为踢出），他们共用一组更宽松的限流额度。

### 通过 Tor 连接

服务器端：`sudo ptterm-ctl install --tor`，结束时会打印 `.onion` 地址（也可以用 `sudo ptterm-ctl status` 查看）。洋葱私钥保存在 `/var/lib/tor/ptterm/`，升级和普通卸载都会保留，只有 `--purge` 会删除。

用户端（需要本地运行 Tor，或者开着 Tor Browser）：

```bash
# Linux / macOS（装好 tor 和 torsocks）
torsocks ssh guest@xxxxxxxx.onion

# 不想装 torsocks：通过 Tor 的 SOCKS 端口（Tor Browser 用 9150，tor 服务用 9050）
ssh -o ProxyCommand='nc -x 127.0.0.1:9050 %h %p' guest@xxxxxxxx.onion

# Windows（Git for Windows 自带 connect.exe），先打开 Tor Browser
ssh -o ProxyCommand="connect -S 127.0.0.1:9150 %h %p" guest@xxxxxxxx.onion
```

洋葱地址本身就包含了服务器的身份验证，不需要再额外核对 SSH 主机指纹。

---

## 性能参考

用自带的压测工具 `cmd/ptt-bench` 测量：所有人都在同一个房间，25 人各每 2 秒发一条消息（全房间约 12.5 条/秒），服务端限制为 **1 个 CPU 核**、`GOMEMLIMIT=600MiB`：

| 在线会话 | 模式 | 投递率 | 延迟 p50 / p99 | 内存 |
| --- | --- | --- | --- | --- |
| 1000 | 完整界面 | 100% | 14 ms / 37 ms | 157 MB |
| 2000 | 完整界面 | 100% | 30 ms / 148 ms | 355 MB |
| 3000 | 完整界面 | 100% | 91 ms / 478 ms | 532 MB |
| 4000 | 纯文本 | 100% | 423 ms / 1.4 s（单核已满载） | 360 MB |

结论：**1 核 1GB 的 VPS 可以承载约 2000 名同时在线的完整界面用户**，3000 人仍能正常投递但延迟开始上升。人更多时加 CPU 核数即可（Go 会自动用满）。同样人数分散在多个房间时开销小得多。

几项关键设计：

- **自己写的轻量终端渲染器。** 利用终端的滚动区域，每条新消息只发送新的那几行，固定行只在内容变化时重画。早期版本用的通用 TUI 框架每帧都要重新排版整屏，300 人在线就要 883 MB 内存，延迟到秒级。
- **渲染结果跨会话共享。** 同一条消息对大多数人显示得一样，只格式化一次。只有你自己发的、或 @ 到你的消息才单独渲染。
- **滚动历史按需渲染。** 每个会话只保存指向共享消息的指针，翻页时才渲染。
- **热路径上没有通用样式引擎。** 颜色是预先算好的转义码，ASCII 文本的宽度直接计算。
- **普通聊天消息不触发状态栏刷新**，不必去争抢全局锁。

自己测：`make bench`，或参考 `cmd/ptt-bench/main.go` 顶部注释。

---

## 配置

配置文件 `/etc/ptterm.env`，改完执行 `systemctl restart ptterm`：

| 环境变量 / 参数 | 默认 | 说明 |
| --- | --- | --- |
| `PTT_ADDR` / `-addr` | `:2222` | 监听地址 |
| `PTT_ADMIN_KEYS` / `-admin-keys` | — | 管理员公钥文件 |
| `PTT_ROOMS` / `-rooms` | 5 个默认频道 | `name:主题,name2:主题2` |
| `PTT_MOTD` / `-motd` | — | 进入时额外显示的公告文本文件 |
| `PTT_HEALTH_ADDR` / `-health-addr` | `127.0.0.1:2223` | `GET /healthz`，只返回计数 |
| `PTT_ADMIN_USER` / `-admin-user` | `admin` | 管理员登录用的用户名 |
| `PTT_ONION_ADDR` / `-onion-addr` | — | 给 Tor 洋葱服务用的本地监听地址（`--tor` 自动设为 `127.0.0.1:2224`） |
| `-history` / `-history-ttl` | 50 / 1h | 每房间历史条数和保留时长 |
| `-max-clients` / `-max-per-ip` / `-max-tcp-per-ip` / `-conn-per-min` | 1000 / 3 / 8 / 10 | 连接限制 |
| `-msg-rate` / `-msg-burst` / `-max-len` | 1/s / 5 / 500 | 发言限制 |
| `-idle-timeout` | 1h | 不发言多久后断开（0 = 不断开） |
| `GOMEMLIMIT` | 内存的 60% | Go 运行时软内存上限 |

其它参数写进 `PTT_ARGS=`。全部参数：`ptterm -h`。

**主机密钥** 在 `/var/lib/ptterm/host_ed25519`，请备份。换了密钥，老用户会看到“主机密钥已变更”警告。

---

## 开发

```bash
make test      # 单元测试 + 真实 SSH 端到端测试
make lint      # go vet + shellcheck + 检查 unit 文件与安装脚本一致
make run       # 本地 127.0.0.1:2222
make dist      # 交叉编译 amd64 / arm64 / armv7 + SHA256SUMS
make bench     # 压测
```

需要 Go 1.26.8+。发布流程：修改 `VERSION` 文件（如 `0.2.1`）并推送到 main，或者推送 `vX.Y.Z` 标签，GitHub Actions 会自动测试、构建三种架构并发布 Release（附 `SHA256SUMS`）。服务器上执行 `sudo ptterm-ctl upgrade` 即可升级。每次推送 main 时，CI 会用 Go 官方校验数据库验证依赖，并在带 systemd 的 Ubuntu 上实际执行一遍安装脚本：安装、聊天、确认拒绝 exec、改端口、卸载。

```
main.go                     参数、健康检查、优雅退出
install.sh                  一键安装 / 升级 / 卸载（也是 ptterm-ctl）
internal/chat/              房间、身份、私聊、历史、限流、封禁、举报（与 SSH 无关）
internal/ui/term.go         完整界面：轻量终端渲染器 + 按键解码
internal/ui/plain.go        纯文本行模式
internal/ui/format.go       消息格式、时区、@提及
internal/server/            Wish SSH 网关、认证、连接限制
cmd/ptt-bench/              压测工具
deploy/                     systemd 单元（由 install.sh --print-unit 生成）和配置示例
```

## 路线图

- [x] SSH 无账号登录、随机昵称、公共频道、私人房间码、私聊、中文
- [x] 限流、管理员工具、举报、屏蔽、消息过期、权限隔离、systemd 沙箱
- [x] 一键安装 / 升级 / 卸载，CI，自动发布，压测
- [x] 普通用户不发送公钥，日志过滤地址，Tor 洋葱服务
- [x] 单核约 2000 名完整界面用户
- [ ] 封禁持久化（可选）、多房间未读提示
- [ ] 与 Private-Talk 打通。普通 SSH 客户端无法端到端加密；要让服务器读不到内容，必须在用户本地有专用加密客户端。
