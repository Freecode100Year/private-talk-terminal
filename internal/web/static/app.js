// VIVANT web client. Talks to /ws: server messages are plain-text output,
// client messages are lines of input. No storage, no third-party code.
"use strict";
(() => {
  const log = document.getElementById("log");
  const form = document.getElementById("form");
  const input = document.getElementById("input");
  const status = document.getElementById("status");
  const again = document.getElementById("reconnect");
  const MAX_LINES = 2000;

  const ssh = document.getElementById("sshcmd");
  if (location.hostname && !/^(localhost|127\.|\[)/.test(location.hostname)) {
    ssh.textContent = "ssh -p 2222 guest@" + location.hostname;
  }

  let ws = null;
  let pending = "";      // output after the last newline
  let history = [];
  let hpos = 0;
  let unread = 0;

  function setStatus(on, text) {
    status.textContent = text;
    status.className = "status " + (on ? "on" : "off");
    again.hidden = on;
    input.disabled = !on;
  }

  function addLine(text, cls) {
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    const div = document.createElement("div");
    div.textContent = text;          // never innerHTML: chat text is untrusted
    if (cls) div.className = cls;
    log.appendChild(div);
    while (log.childElementCount > MAX_LINES) log.firstElementChild.remove();
    if (atBottom) log.scrollTop = log.scrollHeight;
    if (document.hidden) {
      unread++;
      document.title = "(" + unread + ") VIVANT";
    }
  }

  function output(chunk) {
    if (chunk.includes("\x07")) {
      chunk = chunk.replaceAll("\x07", "");
      if (document.hidden && navigator.vibrate) navigator.vibrate(80);
    }
    pending += chunk;
    const parts = pending.split("\n");
    pending = parts.pop();
    for (let l of parts) {
      l = l.replace(/\r$/, "");
      // "[15:04] <nick> text" is chat; notices, help and system lines are dimmed
      addLine(l, /^\[\d\d:\d\d\] </.test(l) || /^\s/.test(l) ? "" : "sys");
    }
  }

  function connect() {
    const url = new URL("ws", location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    setStatus(false, "连接中… · connecting…");
    again.hidden = true;
    ws = new WebSocket(url);
    ws.onopen = () => { setStatus(true, "已连接 · online"); input.focus(); };
    ws.onmessage = (e) => { if (typeof e.data === "string") output(e.data); };
    ws.onclose = () => {
      if (pending) { addLine(pending); pending = ""; }
      addLine("── 连接已断开 · disconnected ──", "sys");
      setStatus(false, "未连接 · offline");
      ws = null;
    };
  }

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const line = input.value;
    if (!line.trim() || !ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(line);
    if (history[history.length - 1] !== line) history.push(line);
    if (history.length > 100) history.shift();
    hpos = history.length;
    input.value = "";
    if (line.trim() === "/quit") ws.close();
  });

  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowUp" && hpos > 0) {
      input.value = history[--hpos];
      e.preventDefault();
    } else if (e.key === "ArrowDown") {
      hpos = Math.min(history.length, hpos + 1);
      input.value = history[hpos] || "";
      e.preventDefault();
    }
  });

  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) { unread = 0; document.title = "VIVANT · 匿名聊天室"; }
  });
  log.addEventListener("click", () => { if (!getSelection().toString()) input.focus(); });
  again.addEventListener("click", connect);
  connect();
})();
