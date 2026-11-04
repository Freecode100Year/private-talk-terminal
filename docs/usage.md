# 连接与使用

[← 返回 README](../README.md)

## 连接方式

| 方式 | 命令 |
| --- | --- |
| 完整界面（推荐） | `ssh -o StrictHostKeyChecking=accept-new -p 2222 guest@HOST` |
| 纯文本模式（老终端 / CMD 乱码时） | `ssh -o StrictHostKeyChecking=accept-new -p 2222 plain@HOST` |
| 无终端 / 脚本 / 机器人 | `ssh -o StrictHostKeyChecking=accept-new -T -p 2222 guest@HOST` |
| 通过 Tor（服务器看不到你的 IP） | `torsocks ssh guest@<onion地址>.onion`（见下文） |

- 用户名随便填（`admin` 留给管理员），没有密码，连上直接进聊天室。
- 使用上面的 `accept-new` 选项后，首次连接会自动记录服务器主机密钥；之后若密钥变化，SSH 会拒绝连接并提示风险。
- 客户端不会向服务器发送你的公钥，不需要额外加 `PubkeyAuthentication=no`（原理见[安全与匿名性](security.md)）。
- Windows 10/11 自带 OpenSSH；中文显示推荐 Windows Terminal。老 CMD 乱码时用 `plain@`，或先执行 `chcp 65001`。

如果希望命令中不写 `-o StrictHostKeyChecking=accept-new`，在 SSH 配置文件中为该域名设置一次：Windows 文件为 `%USERPROFILE%\.ssh\config`，macOS/Linux 文件为 `~/.ssh/config`。把下面内容加到文件靠前位置：

```sshconfig
Host term.freedom8964.com
    StrictHostKeyChecking accept-new
```

之后可直接运行 `ssh -p 2222 guest@term.freedom8964.com`。首次自动保存密钥，后续仍会拒绝密钥变化。

## iPhone / iPad（iOS）

iOS 没有内置的终端工具，需要从 App Store 下载一款第三方 SSH 客户端，再把连接命令里的参数填进去。下面是两种最简单的免费方法。

### 方法一：iSH Shell（最直接，可以直接粘贴命令）

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

### 方法二：Termius 或 WebSSH（图形界面，适合经常连接）

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

## 命令

聊天室里输入 `/help` 只列常用命令，`/help all` 列出全部。

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
/secure                创建端到端加密房间（连服务器都看不到内容），见下文
/link [N]              显示第 N 个加密房间邀请的完整链接（方便复制、点开）
/privacy               查看本服务器存什么、不存什么
/forget                从服务器房间历史中删除你发过的所有消息
/clear  /quit  /help
//text                 发送以 / 开头的普通文字
```

按键：↑↓ 输入历史，PgUp/PgDn 翻看聊天记录，Tab 补全昵称和命令，Esc 清空输入并回到底部，Ctrl+A/E/U/W/K 行编辑，Ctrl+L 重绘。别人 @ 你的消息会高亮。粘贴多行文字会合并成一行，不会误发。

**多房间未读提示：** 顶部房间栏会显示你不在的房间有多少条新消息，例如 `#chinese 32 +5`。如果有人在那边 @了你，会显示成红色的 `@5`，你当前所在的房间里也会出现一行 `@ #chinese <Bob> …  · /join chinese`，可以直接跳过去。中文昵称后面不加空格也能识别（`@夜猫子你好`）。进入那个房间后计数清零。`/rooms` 也会列出 `(5 new, @you)`。私人房间里的 @ 不会通知房外的人，避免泄露房间的存在。

## 通过 Tor 连接（服务器看不到你的 IP）

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
