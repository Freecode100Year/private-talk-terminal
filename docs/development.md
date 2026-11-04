# 开发

[← 返回 README](../README.md)

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
internal/server/            Wish SSH 网关、认证、连接限制、封禁持久化
cmd/ptt-bench/              压测工具
deploy/                     systemd 单元（由 install.sh --print-unit 生成）和配置示例
```

## 路线图

- [x] SSH 无账号登录、随机昵称、公共频道、私人房间码、私聊、中文
- [x] 限流、管理员工具、举报、屏蔽、消息过期、权限隔离、systemd 沙箱
- [x] 一键安装 / 升级 / 卸载，CI，自动发布，压测
- [x] 普通用户不发送公钥，日志过滤地址，Tor 洋葱服务
- [x] 单核约 2000 名完整界面用户
- [x] `/privacy` 数据政策自查、`/forget` 删除自己的消息、禁止核心转储
- [x] 封禁持久化（可选）、`/bans` 与 `/unban`
- [x] 多房间未读提示、跨房间 @提及通知
- [x] 端到端加密房间：`/secure` 转到 HTML Room（钥匙只在用户浏览器里生成），拦截不带口令的邀请链接，`/link` 显示完整链接
- 不计划：在 SSH 聊天室本身里做端到端加密。这需要在用户设备上安装专用客户端，与“一条 ssh 命令就能进”的定位冲突。
