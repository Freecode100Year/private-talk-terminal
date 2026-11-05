// VIVANT web entry: the same rooms and people as `ssh guest@host`, in a
// browser. Messages go over a WebSocket to the server's chat hub; nothing is
// stored in the browser except the font size and background you picked.
'use strict';
(() => {
  const $ = (id) => document.getElementById(id);
  const log = $('log'), input = $('input'), conn = $('conn'), jump = $('jump');
  const MAX_LINES = 1000;
  const KEY_FONT = 'vivant.font', KEY_THEME = 'vivant.theme';

  // Browser storage can be missing or throw (private mode, embedded views).
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, String(v)); } catch { /* ignore */ } },
  };

  let ws = null, palettes = null, status = null, retry = 0, retryTimer = 0, ended = false;
  let firstNick = '', wantNick = '', wantRoom = '', unseen = 0, audio = null;
  const history = []; let histPos = 0;

  // ---------------------------------------------------------------- render --
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  const pad = (n) => String(n).padStart(2, '0');
  const hm = (ev) => {
    if (ev.hm) return ev.hm;
    const d = new Date(ev.ts || Date.now());
    return pad(d.getHours()) + ':' + pad(d.getMinutes());
  };
  const nickColor = (i) => (palettes && status && palettes[status.theme]?.Nicks[i]) || '';

  function nickEl(name, colorIndex, self, hist) {
    const n = el('span', 'nick' + (self ? ' self' : ''), name);
    if (!self && !hist) n.style.color = nickColor(colorIndex); // replayed history stays dim
    if (!hist) n.dataset.c = colorIndex;
    n.title = '@' + name;
    n.addEventListener('click', () => insertAt(name));
    return n;
  }

  // "@nick" tags are underlined (meaning never rides on colour alone). An "@"
  // inside a word (an e-mail address) is left alone, like the terminal UI.
  function withTags(text) {
    const frag = document.createDocumentFragment();
    const re = /(^|[^\p{L}\p{N}_-])(@[\p{L}\p{N}_-]{2,16})/gu;
    let last = 0, m;
    while ((m = re.exec(text))) {
      const start = m.index + m[1].length;
      frag.append(text.slice(last, start), el('span', 'tag', m[2]));
      last = start + m[2].length;
    }
    frag.append(text.slice(last));
    return frag;
  }

  // Server messages carry Chinese and English; show both.
  const bi = (parent, zh, en) => {
    parent.append(zh);
    if (en && en !== zh) parent.append(el('span', 'en', ' · ' + en));
  };

  function render(ev) {
    const li = el('li', ev.kind);
    if (ev.hist) li.classList.add('hist');
    li.append(el('span', 'ts', '[' + hm(ev) + ']'));
    const body = el('span', 'body');
    switch (ev.kind) {
      case 'chat':
        li.append('<', nickEl(ev.from, ev.c, ev.self, ev.hist), '> ');
        if (ev.mention) { li.classList.add('mention'); body.append('» '); }
        body.append(withTags(ev.text));
        li.append(body);
        break;
      case 'action':
        li.append('* ', nickEl(ev.from, ev.c, ev.self, ev.hist), ' ');
        if (ev.mention) { li.classList.add('mention'); body.append('» '); }
        body.append(withTags(ev.text));
        li.append(body);
        break;
      case 'system':
        body.append('-- '); bi(body, ev.text, ev.en); li.append(body);
        break;
      case 'dmin':
        li.append(el('span', 'dm', '[私信 DM ← '), nickEl(ev.from, ev.c, false), el('span', 'dm', '] '), ev.text);
        break;
      case 'dmout':
        li.append(el('span', 'dm', '[私信 DM → ' + ev.to + '] '), ev.text);
        break;
      case 'info':
        body.append('* '); bi(body, ev.text, ev.en); li.append(body);
        break;
      case 'error':
        body.append('! '); bi(body, ev.text, ev.en); li.append(body);
        break;
      case 'notice':
        body.append('📢 [' + (ev.from ? '管理员 Admin ' + ev.from : '系统 System') + '] ');
        bi(body, ev.text, ev.en); li.append(body);
        break;
      case 'mention': {
        li.classList.add('mention-away');
        li.append(el('span', 'head', '@ #' + ev.room + ' <' + ev.from + '>'), ' ', ev.text);
        const go = el('button', 'go', '/join ' + ev.room);
        go.type = 'button';
        go.addEventListener('click', () => say('/join ' + ev.room));
        li.append(go);
        break;
      }
      default:
        bi(body, ev.text, ev.en); li.append(body);
    }
    if (ev.lines && ev.lines.length) {
      const box = el('span', 'lines');
      ev.lines.forEach((l, i) => {
        box.append(l + '\n');
        const en = ev.linesEn && ev.linesEn[i];
        if (en && en !== l) box.append(el('span', 'en', en));
      });
      li.append(box);
    }
    return li;
  }

  const nearBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  function append(li) {
    const stick = nearBottom();
    log.append(li);
    while (log.childElementCount > MAX_LINES) log.firstElementChild.remove();
    if (stick) log.scrollTop = log.scrollHeight; else jump.hidden = false;
  }
  log.addEventListener('scroll', () => { if (nearBottom()) jump.hidden = true; });
  // keep the newest line in view when the phone keyboard opens or the window shrinks
  new ResizeObserver(() => { if (jump.hidden) log.scrollTop = log.scrollHeight; }).observe(log);
  jump.addEventListener('click', () => { log.scrollTop = log.scrollHeight; jump.hidden = true; });

  // ---------------------------------------------------------------- status --
  function applyTheme() {
    if (!palettes || !status) return;
    const p = palettes[status.theme]; if (!p) return;
    const r = document.documentElement.style;
    const vars = {
      '--fg': p.Fg, '--bg': p.Bg, '--head-fg': p.HeaderFg, '--head-bg': p.HeaderBg, '--head-dim': p.HeaderDim,
      '--cur': p.CurRoom, '--prompt': p.Prompt, '--self': p.Self, '--men-fg': p.MentionFg, '--men-bg': p.MentionBg,
      '--tag': p.Tag, '--dim': p.Dim, '--info': p.Info, '--err': p.Error, '--notice': p.Notice, '--dm': p.DM, '--ok': p.OK,
    };
    for (const k in vars) r.setProperty(k, vars[k]);
    document.documentElement.style.colorScheme = status.theme === 'dark' ? 'dark' : 'light';
    log.querySelectorAll('.nick[data-c]:not(.self)').forEach((n) => { n.style.color = nickColor(+n.dataset.c); });
  }

  function applyFont(px) {
    const stick = nearBottom();
    document.documentElement.style.setProperty('--fs', px + 'px');
    if (stick) log.scrollTop = log.scrollHeight;
    $('font-size').textContent = px + 'px';
  }

  function showStatus(st) {
    const prev = status;
    status = st;
    if (!firstNick) firstNick = st.nick;
    else if (st.nick !== firstNick) wantNick = st.nick;     // set by the user: restore after a reconnect
    wantRoom = st.room;
    if (!prev || prev.theme !== st.theme) { applyTheme(); store.set(KEY_THEME, st.theme); }
    if (!prev || prev.font !== st.font) { applyFont(st.font); store.set(KEY_FONT, st.font); }
    $('me').textContent = st.nick + ' · 在线 Online ' + st.online + ' · 运行 Up ' + st.uptime + (st.muted ? ' · 禁言中 MUTED' : '');
    $('topic').textContent = '── ' + st.roomLabel + ' · ' + st.roomUsers + ' 人在线 here' + (st.topic ? ' · ' + st.topic : '');
    const nav = $('rooms');
    nav.replaceChildren(...st.rooms.map((r) => {
      const b = el('button', r.cur ? 'cur' : '');
      b.type = 'button';
      b.append((r.cur ? '▸' : '') + r.label, el('span', 'n', String(r.users)));
      if (r.at) b.append(el('span', 'at', '@' + (r.unread || '')));
      else if (r.unread) b.append(el('span', 'unread', '+' + r.unread));
      b.title = r.cur ? '当前房间 current room' : '/join ' + r.name;
      if (r.cur) b.setAttribute('aria-current', 'true');
      else b.addEventListener('click', () => say('/join ' + r.name));
      return b;
    }));
  }

  // ------------------------------------------------------------ connection --
  function setConn(text, ok) {
    conn.textContent = '● ' + text;
    conn.classList.toggle('off', !ok);
  }

  function connect() {
    clearTimeout(retryTimer);
    ended = false;
    $('rejoin-box').hidden = true;
    setConn('连接中 CONNECTING', false);
    const url = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws';
    const sock = new WebSocket(url);
    ws = sock;
    let restored = false;
    sock.onmessage = (m) => {
      let d; try { d = JSON.parse(m.data); } catch { return; }
      switch (d.t) {
        case 'hello':
          palettes = d.palettes;
          $('ver').textContent = d.version ? 'VIVANT ' + d.version : '';
          if (d.ssh) {
            const s = $('ssh'); s.hidden = false;
            s.replaceChildren('SSH：', el('code', '', d.ssh));
          }
          break;
        case 'st':
          if (!restored) {
            restored = true;
            retry = 0;
            setConn('已连接 CONNECTED', true);
            // carry the page's choices over: saved display settings, and the
            // nickname/room from before a dropped connection
            const f = +store.get(KEY_FONT), th = store.get(KEY_THEME);
            if (f && f !== d.font) send({ t: 'font', v: String(f) });
            if (th && th !== d.theme) send({ t: 'theme', v: th });
            if (wantNick && wantNick !== d.nick) say('/nick ' + wantNick);
            if (wantRoom && wantRoom !== d.room) say('/join ' + wantRoom);
            firstNick = d.nick;
          }
          showStatus(d);
          break;
        case 'ev':
          append(render(d));
          if (d.ring) ring();
          break;
        case 'clear':
          log.replaceChildren();
          break;
        case 'bye':
          ended = true;
          $('bye').textContent = d.reason || '';
          break;
      }
    };
    sock.onclose = () => {
      if (ws !== sock) return;
      if (ended) {
        setConn('已断开 DISCONNECTED', false);
        $('rejoin-box').hidden = false;
        return;
      }
      retry = Math.min(retry + 1, 6);
      const wait = Math.min(30, 2 ** (retry - 1));
      setConn('重连中 RECONNECTING (' + wait + 's)', false);
      retryTimer = setTimeout(connect, wait * 1000);
    };
  }

  const send = (obj) => { if (ws && ws.readyState === 1) ws.send(JSON.stringify(obj)); };
  const say = (text) => send({ t: 'say', text });

  $('rejoin').addEventListener('click', () => { wantNick = ''; wantRoom = ''; firstNick = ''; connect(); });

  // ----------------------------------------------------------------- input --
  $('composer').addEventListener('submit', (e) => {
    e.preventDefault();
    const text = input.value.trim();
    if (!text) return;
    if (ws?.readyState !== 1) { setConn('未连接，稍后再发 NOT CONNECTED', false); return; }
    say(text);
    if (history[history.length - 1] !== text) history.push(text);
    if (history.length > 100) history.shift();
    histPos = history.length;
    input.value = '';
    log.scrollTop = log.scrollHeight;
  });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowUp' && histPos > 0 && !e.isComposing) {
      e.preventDefault(); input.value = history[--histPos];
    } else if (e.key === 'ArrowDown' && !e.isComposing) {
      e.preventDefault();
      histPos = Math.min(history.length, histPos + 1);
      input.value = history[histPos] || '';
    } else if (e.key === 'Escape') {
      input.value = ''; log.scrollTop = log.scrollHeight;
    }
  });
  function insertAt(nick) {
    const v = input.value;
    input.value = (v && !v.endsWith(' ') ? v + ' ' : v) + '@' + nick + ' ';
    input.focus();
  }

  // Display settings: the server keeps them for this session (so /font and
  // /theme typed in the chat do the same), the browser remembers them.
  $('font-up').addEventListener('click', () => send({ t: 'font', v: '+' }));
  $('font-down').addEventListener('click', () => send({ t: 'font', v: '-' }));
  $('theme').addEventListener('click', () => {
    const order = ['green', 'dark', 'light'];
    const cur = status ? order.indexOf(status.theme) : 0;
    send({ t: 'theme', v: order[(cur + 1) % order.length] });
  });
  document.addEventListener('keydown', (e) => {
    if (!(e.ctrlKey || e.metaKey) || !e.altKey) return; // Ctrl/⌘+Alt + = / - : page font without zooming the browser
    if (e.key === '=' || e.key === '+') { e.preventDefault(); send({ t: 'font', v: '+' }); }
    if (e.key === '-') { e.preventDefault(); send({ t: 'font', v: '-' }); }
  });

  // ---------------------------------------------------------------- notify --
  function ring() {
    if (status && !status.bell) return;
    if (document.hidden) { unseen++; document.title = '(' + unseen + ') VIVANT'; }
    try { navigator.vibrate?.(80); } catch { /* ignore */ }
    try { // a short beep, like the terminal bell; only after the page was used
      audio = audio || new AudioContext();
      if (audio.state !== 'running') return;
      const o = audio.createOscillator(), g = audio.createGain();
      o.frequency.value = 880; g.gain.value = 0.05;
      o.connect(g).connect(audio.destination);
      o.start(); o.stop(audio.currentTime + 0.12);
    } catch { /* ignore */ }
  }
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) { unseen = 0; document.title = 'VIVANT · 匿名聊天室'; }
  });
  document.addEventListener('pointerdown', () => { try { audio = audio || new AudioContext(); audio.resume(); } catch { /* ignore */ } }, { once: true });

  // show saved settings at once, before the server confirms them
  const f0 = +store.get(KEY_FONT);
  if (f0 >= 12 && f0 <= 40) applyFont(f0);
  connect();
  input.focus();
})();
