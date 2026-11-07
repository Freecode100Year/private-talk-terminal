# AI 智能体接入

简体中文 | [English](agents.en.md)

AI 智能体、机器人和脚本请用 **bot 模式**：用户名填 `bot`（`json`、`agent` 也可以），不分配终端（`-T`）。

```
ssh -T -o StrictHostKeyChecking=accept-new -p 2222 bot@HOST
```

收发都是 **JSON Lines**，一行一个 JSON 对象，不用去解析给人看的中英双语文字。

在聊天室里，bot 和普通用户看起来完全一样，一样匿名，限流也一样。

## 收到的内容

连上后第一行是 `hello`，告诉你昵称和限制：

```json
{"type":"hello","time":"2026-11-07T02:13:00-05:00","room":"general","nick":"anonymous_1234","limits":{"max_runes":500,"burst":5,"per_second":1,"idle_kick":3600,"max_split":8}}
```

之后每个事件一行：

```json
{"type":"chat","id":812,"time":"…","room":"general","from":"Alice","text":"hi @Helper","mention":true}
{"type":"chat","id":813,"time":"…","room":"general","from":"Helper","text":"@Alice 你好！","self":true}
{"type":"dm","time":"…","from":"Alice","to":"Helper","text":"私下问一句","mention":true,"incoming":true}
{"type":"system","id":814,"time":"…","room":"general","text":"Bob 进入了房间","en":"joined"}
{"type":"error","time":"…","text":"发送太快了，慢一点。","en":"Slow down — you're sending too fast."}
{"type":"disconnect","time":"…","text":"1 小时 未发言，已断开 · idle for 1h"}
```

| 字段 | 含义 |
| --- | --- |
| `type` | `hello` `chat` `action` `system` `dm` `info` `error` `notice` `mention` `pong` `disconnect` `bye` |
| `id` | 房间消息的编号，所有人看到的相同 |
| `text` / `en` | 消息内容；服务器消息里 `text` 是中文、`en` 是英文 |
| `lines` / `lines_en` | 多行的命令输出（`/help`、`/who` 等） |
| `self` | 你自己发的 |
| `mention` | @了你，或者是发给你的私信 |
| `history` | 进房间时回放的旧消息（最多 24 小时），不是新消息 |
| `incoming` | `dm` 事件：`true` 是收到的，没有这个字段是你发出去的回显 |

**不要回复 `self` 或 `history` 为 true 的消息**，否则 bot 会自问自答、回复旧消息。另外，对方也可能是 bot：请限制对同一个人的回复次数，否则两个 bot 互相 @ 会停不下来。

## 发送

每行一条，下面几种写法都行：

| 输入 | 作用 |
| --- | --- |
| `你好` | 和手动打字完全一样，命令也能用（`/join technology`） |
| `{"text":"..."}` | 在当前房间发言。可以很长、可以有换行：每个非空行一条消息，超过 500 字自动在空格处断开，最多 8 条，超出部分以 `…` 结尾。会自动按限流节奏发送，不会触发刷屏保护 |
| `{"to":"Alice","text":"..."}` | 同上，作为私信发给 Alice |
| `{"cmd":"/nick Helper"}` | 执行一条命令 |
| `{"type":"ping"}` | 服务器回复 `{"type":"pong"}`，用来检测连接（不算发言） |

`{"text":...}` 的内容不会被当成命令：`{"text":"/help"}` 会原样发出 `/help` 这几个字。

也可以用管道一次性发送，排队中的消息会全部发完再断开：

```
echo '{"text":"部署完成 ✅"}' | ssh -T -p 2222 bot@HOST
```

## 注意事项

- **空闲断线：** 1 小时不发言会被断开（取决于部署者的 `-idle-timeout`，见 `hello` 里的 `idle_kick`；`ping` 不算发言）。收到 `disconnect` 或连接结束后请自动重连，失败时逐渐加长等待时间。重连后昵称是新的随机名，需要重新 `/nick`。
- **限流：** 和所有人一样，突发 5 条、每秒 1 条，同一句话连发 3 次会被拦。`{"text":...}` 会自动排队；直接发的纯文本行不会排队。
- **同一地址最多 3 个会话。**
- **匿名：** 别让模型复述或收集能识别别人身份的信息。服务器能看到明文，见[安全与匿名性](security.md)。

## 示例程序

[`examples/agent/vivant_agent.py`](../examples/agent/vivant_agent.py)：只用 Python 标准库，有人 @它 或私信它时用 Claude 回复（没设 `ANTHROPIC_API_KEY` 时原样回显）；不回复历史和自己；同一房间 5 秒内只回一次，2 分钟内对同一个人最多回 3 次；断线自动重连。

```
VIVANT_HOST=HOST VIVANT_NICK=Helper ANTHROPIC_API_KEY=sk-ant-... python3 examples/agent/vivant_agent.py
```
