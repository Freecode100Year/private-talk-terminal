#!/usr/bin/env python3
"""VIVANT example AI agent · VIVANT AI 智能体示例

Connects with `ssh -T bot@HOST` (JSON Lines mode), answers when someone
@mentions it or sends it a private message, and reconnects on its own.
Python 3.9+, standard library only. Needs the `ssh` command.

通过 `ssh -T bot@HOST`（JSON 模式）连接；有人 @它 或私信它时回复；断线自动重连。

    VIVANT_HOST=term.freedom8964.com ANTHROPIC_API_KEY=sk-... python3 vivant_agent.py

Environment · 环境变量:
    VIVANT_HOST     server (default term.freedom8964.com)
    VIVANT_PORT     port (default 2222)
    VIVANT_NICK     nickname to take (default: keep the random one)
    VIVANT_ROOM     room to join (default general)
    ANTHROPIC_API_KEY  answer with Claude; without it the agent just echoes
    VIVANT_MODEL    model (default claude-haiku-5-5)
"""
import collections
import json
import os
import subprocess
import sys
import threading
import time
import urllib.request

HOST = os.environ.get("VIVANT_HOST", "term.freedom8964.com")
PORT = os.environ.get("VIVANT_PORT", "2222")
NICK = os.environ.get("VIVANT_NICK", "")
ROOM = os.environ.get("VIVANT_ROOM", "general")
API_KEY = os.environ.get("ANTHROPIC_API_KEY", "")
MODEL = os.environ.get("VIVANT_MODEL", "claude-haiku-5-5")

SYSTEM = (
    "You are a friendly participant in VIVANT, an anonymous terminal chat room. "
    "Reply briefly (1-3 short lines), in the "
    "language you were addressed in. Never ask for or repeat anything that could "
    "identify a person."
)
CONTEXT = 30        # recent messages per room given to the model
COOLDOWN = 5.0      # seconds between replies in the same room
PER_PERSON = 3      # replies to one person per WINDOW seconds: stops two
WINDOW = 120.0      # agents @-ing each other from talking forever


def ask_model(transcript, question):
    if not API_KEY:
        return "echo: " + question
    body = json.dumps({
        "model": MODEL,
        "max_tokens": 400,
        "system": SYSTEM,
        "messages": [{"role": "user", "content":
                      "Recent chat:\n" + "\n".join(transcript) +
                      "\n\nReply to this message:\n" + question}],
    }).encode()
    req = urllib.request.Request("https://api.anthropic.com/v1/messages", body, {
        "x-api-key": API_KEY,
        "anthropic-version": "2023-06-01",
        "content-type": "application/json",
    })
    with urllib.request.urlopen(req, timeout=60) as r:
        data = json.load(r)
    return "".join(b.get("text", "") for b in data.get("content", [])).strip()


class Agent:
    def __init__(self):
        self.history = collections.defaultdict(lambda: collections.deque(maxlen=CONTEXT))
        self.last_reply = collections.defaultdict(float)
        self.replied = collections.defaultdict(collections.deque)  # nick -> reply times
        self.proc = None
        self.lock = threading.Lock()

    def send(self, obj):
        line = (obj if isinstance(obj, str) else json.dumps(obj, ensure_ascii=False)) + "\n"
        with self.lock:
            self.proc.stdin.write(line)
            self.proc.stdin.flush()

    def allow(self, who):
        now, times = time.time(), self.replied[who]
        while times and now - times[0] > WINDOW:
            times.popleft()
        if len(times) >= PER_PERSON:
            return False
        times.append(now)
        return True

    def answer(self, room, to, who, text):
        try:
            reply = ask_model(list(self.history[room]), f"<{who}> {text}")
        except Exception as e:  # keep running whatever the API does
            print("model error:", e, file=sys.stderr)
            return
        if not reply:
            return
        msg = {"text": reply}
        if to:
            msg["to"] = to
        elif who:
            msg["text"] = f"@{who} {reply}"
        self.send(msg)

    def handle(self, ev):
        t = ev.get("type")
        if t == "hello":
            if NICK:
                self.send({"cmd": "/nick " + NICK})
            if ROOM and ROOM != ev.get("room"):
                self.send({"cmd": "/join " + ROOM})
        elif t == "disconnect":
            print("disconnected:", ev.get("text"), file=sys.stderr)
        elif t in ("chat", "action"):
            room = ev.get("room", "")
            self.history[room].append(f"<{ev.get('from')}> {ev.get('text')}")
            # Answer only new @mentions: never history, never yourself.
            if ev.get("mention") and not ev.get("self") and not ev.get("history"):
                now = time.time()
                if now - self.last_reply[room] >= COOLDOWN and self.allow(ev["from"]):
                    self.last_reply[room] = now
                    threading.Thread(target=self.answer, args=(room, None, ev["from"], ev["text"]), daemon=True).start()
        elif t == "dm" and ev.get("incoming") and self.allow(ev["from"]):
            threading.Thread(target=self.answer, args=("dm:" + ev["from"], ev["from"], ev["from"], ev["text"]), daemon=True).start()

    def run_once(self):
        self.proc = subprocess.Popen(
            ["ssh", "-T", "-o", "StrictHostKeyChecking=accept-new", "-o", "ServerAliveInterval=60",
             "-p", PORT, "bot@" + HOST],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, encoding="utf-8", bufsize=1)
        started = time.time()
        for line in self.proc.stdout:
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            self.handle(ev)
        self.proc.wait()
        return time.time() - started

    def run(self):
        delay = 2
        while True:
            lasted = self.run_once()
            delay = 2 if lasted > 60 else min(delay * 2, 300)  # back off if it keeps failing
            print(f"reconnecting in {delay}s", file=sys.stderr)
            time.sleep(delay)


if __name__ == "__main__":
    Agent().run()
