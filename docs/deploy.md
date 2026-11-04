# 部署与配置

[← 返回 README](../README.md)

## 一键安装

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
| `--alt-port N` | 同时监听第二个端口，例如 `443`：公司、学校、酒店网络常只放行 80/443。机器上已有网站占用 443 时用不了，安装器会检查并提示；`--alt-port off` 关闭 |
| `--tor` | 同时发布为 Tor 洋葱服务（自动安装 Tor），用户可以不暴露 IP 连接，见[安全与匿名性](security.md) |
| `--persist-bans` | 封禁在重启和升级后依然有效（默认只存在内存里）；`--no-persist-bans` 关闭。隐私上的取舍见[安全与匿名性](security.md) |
| `--from-source` | 从源码编译（会临时下载 Go，结束后删除） |
| `--no-firewall` | 不改防火墙 |

网络受限时：`PTT_DOWNLOAD_BASE=<镜像地址>` 指向放有 `ptterm-linux-<arch>` 和 `SHA256SUMS` 的目录；源码编译可设 `GOPROXY=https://goproxy.cn`。

**还需要你手动做的两件事：**

- 云厂商的防火墙（Oracle VCN 安全列表、AWS/GCP 安全组等）放行 TCP 端口。
- 域名 A/AAAA 记录指向 VPS。在 Cloudflare 上必须设为 **DNS only（灰云）**，橙云代理不转发 SSH。

## 公布主机指纹

用户第一次连接时，SSH 会显示服务器的主机密钥指纹。请把指纹公布在用户能看到的地方（例如 README、网站），用户核对一致才能确认没有被冒充：

```bash
sudo ptterm-ctl status        # 输出里的 Host key: SHA256:…
```

主机密钥在 `/var/lib/ptterm/host_ed25519`，务必备份。换了密钥，指纹就会变，老用户会看到警告。

## 管理员

用户名填 `admin`，并用安装时登记的私钥连接（`ssh -p 2222 -i ~/.ssh/id_ed25519 admin@HOST`），分隔线上会显示 `[ADMIN]`。只有 `admin` 这个用户名才会检查密钥：管理员的密钥配错了会直接被拒绝，不会悄悄降级成普通用户。

```
/kick <nick> [reason]   /mute <nick> [分钟]   /unmute <nick>
/ban <nick> [分钟]      /bans   /unban <id>   /unbanall
/broadcast <text>       /topic <text>   /reports
```

封禁对象是“地址”的匿名哈希（见[安全与匿名性](security.md)）。`/bans` 列出封禁的哈希前缀和剩余时间，`/unban <id>` 解除。默认只存在内存里、重启后清空；安装时加 `--persist-bans` 可以让封禁跨重启保留。

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
| `PTT_BAN_FILE` / `-ban-file` | — | 封禁持久化文件（`--persist-bans` 设为 `/var/lib/ptterm/bans.json`） |
| `PTT_ALT_ADDR` / `-alt-addr` | — | 额外监听地址，逗号分隔（`--alt-port 443` 设为 `:443`） |
| `PTT_TOR_EXITS` / `-tor-exits` | `on` | 定期下载 Tor 出口节点列表，让 Tor 用户共用宽松限额；`off` 关闭 |
| `-quiet-above` | 20 | 房间超过这个人数后，进出提示改为每 30 秒汇总一次（-1 = 不汇总） |
| `PTT_SECURE_URL` / `-secure-url` | `https://hr50.freedom8964.com/` | `/secure` 指向的 HTML Room 创建页；其他站点的邀请会显示警告 |
| `-history` / `-history-ttl` | 50 / 1h | 每房间历史条数和保留时长 |
| `-max-clients` / `-max-per-ip` / `-max-tcp-per-ip` / `-conn-per-min` | 1000 / 3 / 8 / 10 | 连接限制 |
| `-msg-rate` / `-msg-burst` / `-max-len` | 1/s / 5 / 500 | 发言限制 |
| `-idle-timeout` | 1h | 不发言多久后断开（0 = 不断开） |
| `GOMEMLIMIT` | 内存的 60% | Go 运行时软内存上限 |

其它参数写进 `PTT_ARGS=`。全部参数：`ptterm -h`。

**主机密钥** 在 `/var/lib/ptterm/host_ed25519`，请备份。换了密钥，老用户会看到“主机密钥已变更”警告。
