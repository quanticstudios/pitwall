'use strict';
// invariant: daemon text goes in with textContent, never as HTML.

const KEY = 'pitwall-token';
const list = document.getElementById('list');
const status = document.getElementById('status');
const push = document.getElementById('push');
const full = document.getElementById('full');
let token = '';
try { token = localStorage.getItem(KEY) || ''; } catch (e) { /* private mode: pair again each visit */ }
let timer = 0;
const cards = new Map(); // item key: card element
let focusPane = ''; // a tapped push's pane, shown once its card is

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function saveToken(t) {
  token = t;
  try { t ? localStorage.setItem(KEY, t) : localStorage.removeItem(KEY); } catch (e) { /* kept for this visit */ }
}

function unpaired(msg) {
  saveToken('');
  full.hidden = true;
  cards.clear();
  push.replaceChildren();
  const box = el('div', 'empty');
  const how = el('p', '', 'On your computer, run ');
  how.append(el('code', '', 'pitwall remote pair'), ' or open Settings, Phone, and scan the code it shows, or enter its code here.');
  // why: an app added to the Home Screen keeps its own storage, so it pairs on its own, and it cannot scan.
  const form = el('form', 'row');
  const input = el('input');
  input.placeholder = 'Pairing code';
  input.setAttribute('aria-label', input.placeholder);
  input.autocapitalize = 'characters';
  input.autocomplete = 'off';
  form.append(input, el('button', 'send', 'Pair'));
  form.onsubmit = (e) => { e.preventDefault(); pair(input.value); };
  box.append(el('p', '', msg || 'This phone is not paired.'), how, form);
  list.replaceChildren(box);
  status.textContent = '';
  document.title = 'pitwall';
}

async function pair(code) {
  code = code.toUpperCase().replace(/[^A-Z2-7]/g, '');
  try {
    const r = await fetch('/api/pair', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code }) });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    saveToken((await r.json()).token);
  } catch (e) {
    unpaired(e.message);
    return;
  }
  schedule(0);
  setupPush();
}

async function api(path, body) {
  const r = await fetch(path, {
    method: body ? 'POST' : 'GET',
    headers: { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
    cache: 'no-store',
  });
  if (r.status === 401) {
    unpaired('This phone is no longer paired.');
    throw new Error('unpaired');
  }
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.json();
}

const agents = { claude: 'Claude Code', codex: 'Codex', pi: 'pi' };

function card(it) {
  const c = el('section', 'card');
  c.dataset.pane = it.pane;
  const top = el('div', 'top');
  top.append(el('span', 'pill ' + it.state, it.label), el('span', 'tab', it.tab || 'Tab'));
  c.append(top, el('div', 'where', [agents[it.agent] || it.agent, it.session].filter(Boolean).join(' · ')));
  if (it.detail && !it.requests) c.append(el('p', 'detail', it.detail));
  if (it.risk || it.advice) {
    const f = el('div', 'flags');
    if (it.risk) f.append(el('span', 'risk', 'Risk: ' + it.risk));
    if (it.advice) f.append(el('span', 'advice', 'Model: ' + it.advice));
    c.append(f);
  }
  const note = el('div', 'note');
  const done = (msg) => { note.className = 'note'; note.textContent = msg; schedule(800); };
  const failed = (e) => { if (e.message !== 'unpaired') { note.className = 'note bad'; note.textContent = e.message; } };
  if (it.answer) {
    const row = el('div', 'row');
    const allow = el('button', 'allow', 'Allow');
    const deny = el('button', '', 'Deny');
    const send = (yes) => {
      allow.disabled = deny.disabled = true;
      api('/api/answer', { pane: it.pane, at: it.at, allow: yes })
        .then(() => done(yes ? 'Allowed.' : 'Denied.'))
        .catch((e) => { allow.disabled = deny.disabled = false; failed(e); });
    };
    allow.onclick = () => send(true);
    deny.onclick = () => send(false);
    row.append(allow, deny);
    c.append(row);
  }
  if (it.reply) {
    const row = el('div', 'row');
    const text = el('textarea');
    text.rows = 1;
    text.placeholder = it.state === 'awaiting-input' ? 'Answer' : 'Reply';
    text.setAttribute('aria-label', text.placeholder);
    const send = el('button', 'send', 'Send');
    send.onclick = () => {
      if (!text.value.trim()) return;
      send.disabled = true;
      api('/api/reply', { pane: it.pane, at: it.at, text: text.value })
        .then(() => { text.value = ''; done('Sent.'); })
        .catch((e) => { send.disabled = false; failed(e); });
    };
    row.append(text, send);
    c.append(row);
  }
  (it.requests || []).forEach((r) => c.append(request(it.pane, r, done, failed)));
  c.append(note);
  return c;
}

// request is a chat's pending approval, plan or question, answered by its id.
function request(chat, r, done, failed) {
  const box = el('div', 'request');
  if (r.tool || r.command) box.append(el('pre', 'command', [r.tool, r.command].filter(Boolean).join(': ')));
  if (r.path) box.append(el('div', 'where', r.path));
  if (r.detail) box.append(el('p', 'detail', r.detail));
  const buttons = [];
  const lock = (on) => buttons.forEach((b) => { b.disabled = on; });
  if (r.kind === 'input') {
    const picks = (r.questions || []).map((q) => {
      box.append(el('p', 'detail', q.prompt || q.header || ''));
      const pick = { id: q.id, label: '' };
      if (q.options && q.options.length) {
        const row = el('div', 'row wrap');
        q.options.forEach((o) => {
          const b = el('button', '', o.label);
          if (o.description) b.title = o.description;
          b.onclick = () => { pick.label = o.label; row.querySelectorAll('button').forEach((x) => x.classList.toggle('allow', x === b)); };
          buttons.push(b);
          row.append(b);
        });
        box.append(row);
      }
      pick.text = el('textarea');
      pick.text.rows = 1;
      pick.text.placeholder = q.options && q.options.length ? 'Or type an answer' : 'Answer';
      pick.text.setAttribute('aria-label', pick.text.placeholder);
      const typed = el('div', 'row');
      typed.append(pick.text);
      box.append(typed);
      return pick;
    });
    const send = el('button', 'send allow', 'Send');
    buttons.push(send);
    send.onclick = () => {
      const answers = {};
      for (const p of picks) {
        const v = p.text.value.trim() || p.label;
        if (!v) { failed(new Error('Answer every question.')); return; }
        answers[p.id] = [v];
      }
      lock(true);
      api('/api/input', { chat, request: r.id, answers })
        .then(() => done('Sent.'))
        .catch((e) => { lock(false); failed(e); });
    };
    const row = el('div', 'row');
    row.append(send);
    box.append(row);
    return box;
  }
  const reason = el('textarea');
  reason.rows = 1;
  reason.placeholder = 'Reason to deny (optional)';
  reason.setAttribute('aria-label', reason.placeholder);
  const answer = (decision, msg) => {
    lock(true);
    api('/api/approve', { chat, request: r.id, decision, reason: decision === 'deny' ? reason.value.trim() : '' })
      .then(() => done(msg))
      .catch((e) => { lock(false); failed(e); });
  };
  const row = el('div', 'row wrap');
  const allow = el('button', 'allow', r.kind === 'plan' ? 'Approve plan' : 'Allow');
  allow.onclick = () => answer('allow', 'Allowed.');
  buttons.push(allow);
  row.append(allow);
  if (r.kind !== 'plan') {
    const session = el('button', '', 'Allow for session');
    session.onclick = () => answer('allow-session', 'Allowed for this session.');
    buttons.push(session);
    row.append(session);
  }
  const deny = el('button', '', r.kind === 'plan' ? 'Keep planning' : 'Deny');
  deny.onclick = () => answer('deny', 'Denied.');
  buttons.push(deny);
  row.append(deny);
  const why = el('div', 'row');
  why.append(reason);
  box.append(row, why);
  return box;
}

// why: an unchanged item keeps its card, so a reply being typed survives a refresh.
function render(items, device) {
  const keep = new Map();
  const want = items.map((it) => {
    const k = [it.pane, it.at, it.risk, it.advice, (it.requests || []).map((r) => r.id)].join('|');
    const c = cards.get(k) || card(it);
    keep.set(k, c);
    return c;
  });
  cards.clear();
  keep.forEach((c, k) => cards.set(k, c));
  if (!want.length) want.push(el('div', 'empty', 'Nothing needs you.'));
  const same = want.length === list.children.length && want.every((c, i) => list.children[i] === c);
  if (!same) list.replaceChildren(...want);
  if (focusPane) {
    const c = want.find((c) => c.dataset.pane === focusPane);
    focusPane = '';
    if (c) {
      c.scrollIntoView({ block: 'center' });
      c.classList.add('focus');
      setTimeout(() => c.classList.remove('focus'), 2000);
    }
  }
  document.title = items.length ? '(' + items.length + ') pitwall' : 'pitwall';
  status.textContent = (device ? device + ' · ' : '') + new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

async function refresh() {
  if (!token) return;
  try {
    const r = await api('/api/items');
    full.hidden = !r.web;
    render(r.items || [], r.device);
  } catch (e) {
    if (e.message !== 'unpaired') status.textContent = 'Offline: ' + e.message;
  }
}

function schedule(ms) {
  clearTimeout(timer);
  if (!token || document.hidden) return;
  timer = setTimeout(async () => { await refresh(); schedule(3000); }, ms);
}

document.addEventListener('visibilitychange', () => schedule(0));

// why: the device token stays in this page; the full client gets a session cookie of its own.
full.querySelector('button').onclick = async () => {
  try {
    location.assign((await api('/api/web', {})).url);
  } catch (e) {
    if (e.message !== 'unpaired') status.textContent = e.message;
  }
};

function note(text, button) {
  const p = el('p', 'note', text);
  if (button) p.append(button);
  push.replaceChildren(p);
}

function keyBytes(k) {
  const s = atob(k.replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(s, (ch) => ch.charCodeAt(0));
}

// setupPush offers notifications, or says why this browser gets none.
async function setupPush() {
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) {
    if (/iPhone|iPad/.test(navigator.userAgent) && !navigator.standalone) {
      note('For notifications on iPhone: Share, Add to Home Screen, then open pitwall from the Home Screen and pair it there.');
    } else if (!window.isSecureContext) {
      note('Notifications need HTTPS with a certificate this phone trusts, such as the one tailscale serve gives.');
    }
    return;
  }
  let reg, r, sub;
  try {
    reg = await navigator.serviceWorker.register('sw.js');
    r = await api('/api/push');
    sub = await reg.pushManager.getSubscription();
  } catch (e) {
    if (e.message !== 'unpaired') note('Notifications are off: ' + e.message + ' They need a certificate this phone trusts, such as the one tailscale serve gives.');
    return;
  }
  if (sub && Notification.permission === 'granted') {
    push.replaceChildren();
    if (!r.subscribed) api('/api/push', sub.toJSON()).catch(() => {});
    return;
  }
  if (Notification.permission === 'denied') {
    note('Notifications are blocked for this page in the browser settings.');
    return;
  }
  const on = el('button', 'send', 'Turn on');
  on.onclick = async () => {
    on.disabled = true;
    try {
      if (await Notification.requestPermission() !== 'granted') throw new Error('not allowed.');
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(r.key) });
      await api('/api/push', sub.toJSON());
      note('Notifications are on.');
    } catch (e) {
      on.disabled = false;
      if (e.message !== 'unpaired') note('Notifications: ' + e.message, on);
    }
  };
  note('Get a notification when an agent needs you.', on);
}

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.addEventListener('message', (e) => {
    focusPane = (e.data && e.data.pane) || '';
    schedule(0);
  });
}

async function start() {
  const hash = new URLSearchParams(location.hash.slice(1));
  focusPane = hash.get('pane') || '';
  if (location.hash) history.replaceState(null, '', location.pathname);
  const code = hash.get('pair');
  if (code) {
    await pair(code);
    return;
  }
  if (!token) {
    unpaired();
    return;
  }
  schedule(0);
  setupPush();
}

start();
