# VIVANT · 匿名终端聊天室

简体中文 | [English](README.en.md)

[![CI](https://github.com/Freecode100Year/vivant/actions/workflows/ci.yml/badge.svg)](https://github.com/Freecode100Year/vivant/actions/workflows/ci.yml)

一条 SSH 命令进入聊天室。不用注册、不用密码、不用装 App。

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

所有提示、帮助和按键说明都同时显示简体中文和英文。

## 怎么连

**电脑**（Windows / macOS / Linux 自带 ssh）：运行上面那条命令。第一次连接会问是否信任服务器，输入 `yes`。

**iPhone / iPad**：

- **iSH Shell**：App Store 免费下载，先运行 `apk add openssh-client`，再运行上面的命令。
- **Termius**：新建主机，地址 `term.freedom8964.com`，端口 `2222`，用户名 `guest`，密码留空。

网络只放行 443 时，把 `-p 2222` 换成 `-p 443`（需部署者开启）。用户名随便填，没有密码。屏幕太窄或显示乱码时，把用户名换成 `plain`，进入纯文本模式。更多连接方式（Tor、脚本、免确认指纹）见[连接与使用](docs/usage.md)。

## 常用命令

直接打字就是发言。

```
/rooms               有哪些房间
/join chinese        进入房间
/msg <昵称> <内容>   私聊
/nick <名字>         改昵称
/who                 谁在这里
/help all            全部命令
```

## 隐私

- 没有账号；你的 SSH 公钥不会发给服务器；日志里没有 IP、昵称和聊天内容；消息只在内存里，过期自动删除。
- **服务器能看到聊天内容**，别发能认出你的信息。在聊天室里输入 `/privacy` 可以查看这台服务器存什么、不存什么，`/forget` 删除你发过的消息。
- 想连 IP 也不让服务器看到：通过 Tor 连接（需要部署者开启）。

详细说明见[安全与匿名性](docs/security.md)。

## 部署自己的聊天室

在一台 Linux VPS 上运行：

```bash
curl -fsSL https://raw.githubusercontent.com/Freecode100Year/vivant/main/install.sh | sudo bash
```

装好后用 `sudo vivant-ctl status | upgrade | uninstall` 管理；连不上时运行 `sudo vivant-ctl doctor`，它会逐项检查并告诉你怎么修。别忘了在云厂商的防火墙里放行 TCP 2222；域名在 Cloudflare 上要设为 DNS only（灰云）。也可以用 Docker 部署，见[部署与配置](docs/deploy.md#docker可选)。

**服务器连不上 GitHub：** 下载发布页里的离线一键安装包，在自己电脑上运行 `bash vivant-<版本>-linux-offline.sh --to root@服务器` 即可部署，见[离线安装](docs/deploy.md#离线安装服务器连不上-github)。想在 Windows / macOS 上临时跑一个（测试、局域网），发布页也有对应的程序，见[部署与配置](docs/deploy.md#windows--macos)。

**从 Private-Talk Terminal 升级：** VIVANT 以前叫 Private-Talk Terminal（`ptterm`）。在旧服务器上运行一次上面的命令即可自动迁移，主机密钥、配置、封禁、洋葱地址都会保留，详见[部署与配置](docs/deploy.md#从-private-talk-terminal-迁移)。

单核 1GB 的 VPS 大约能承载 2000 人同时在线。安装参数、管理员、配置项见[部署与配置](docs/deploy.md)。

## 计划

参考同类项目（ssh-chat、devzat、SoftRoom、sshoosh、SSHChat）后定下的改进，按顺序进行。原则：不增加新用户要学的东西，不削弱匿名。

- [x] **被 @、收到私聊时响铃**：默认开启，`/bell off` 关闭（借鉴 devzat）
- [x] **人多时折叠进出提示**：合并成“3 人进入、2 人离开”（借鉴 ssh-chat `/quiet`）
- [x] **备用 443 端口**：安装时加 `--alt-port 443`，给被防火墙拦住的用户一条路（借鉴 devzat）
- [x] **核对主机指纹、`ssh chat` 短命令**：写进文档，`vivant-ctl status` 显示指纹（借鉴 ssh-chat、devzat）
- [x] **Tor 出口 IP 共用宽松限额**：避免误伤用 Tor 浏览器直连的人（反向借鉴 devzat，它拒绝 Tor 用户）
- [x] **界面中英双语**：所有提示、`/help`、按键说明、安装脚本输出同时标注简体中文和英文
- [x] **`vivant-ctl doctor` 自检**：逐项检查服务、端口、防火墙、域名解析、Cloudflare 灰云（借鉴 sshoosh）
- [x] **可选加固**：连接层读取限速、Docker 镜像（借鉴 ssh-chat、sshoosh）
- [x] **握手超时**：30 秒内没进入聊天的连接直接断开，扫描器占不住名额；443 等知名端口在安装和 `doctor` 时给出提醒
- [x] **外部监控、口令设计原则**：写进文档（借鉴 devzat、ssh-chat、sshoosh）
- [x] **安全审计后的加固**：只用现代 SSH 算法、剔除隐形字符和标签字符（附模糊测试）、TCP 总连接上限、没有生效封禁时自动换封禁密钥
- [x] **改名 VIVANT**：旧服务器运行一键安装命令自动迁移
- [x] **离线一键安装**：一个自解压文件，`--to root@服务器` 从自己电脑直接部署；另附 Windows / macOS 程序供测试和局域网使用

## 文档

- [连接与使用](docs/usage.md)：各平台连接方法、iPhone 详细步骤、全部命令、Tor
- [安全与匿名性](docs/security.md)：部署者能看到什么、不能保证什么
- [部署与配置](docs/deploy.md)：一键安装参数、迁移、管理员、配置项、Docker
- [性能](docs/performance.md)：压测数据和设计
- [开发](docs/development.md)：构建、测试、发布、代码结构、路线图

纯 Go 单个静态二进制，无数据库，MIT 许可。
