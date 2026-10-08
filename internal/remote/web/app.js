'use strict';
// invariant: daemon text goes in with textContent, never as HTML.

const KEY = 'pitwall-token';
const list = document.getElementById('list');
const status = document.getElementById('status');
let token = '';
try { token = localStorage.getItem(KEY) || ''; } catch (e) { /* private mode: pair again each visit */ }
let timer = 0;
const cards = new Map(); // item key: card element

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
  cards.clear();
  const box = el('div', 'empty');
  const how = el('p', '', 'On your computer, run ');
  how.append(el('code', '', 'pitwall remote pair'), ' or open Settings, Phone, and scan the code it shows.');
  box.append(el('p', '', msg || 'This phone is not paired.'), how);
  list.replaceChildren(box);
  status.textContent = '';
  document.title = 'pitwall';
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
  const top = el('div', 'top');
  top.append(el('span', 'pill ' + it.state, it.label), el('span', 'tab', it.tab || 'Tab'));
  c.append(top, el('div', 'where', [agents[it.agent] || it.agent, it.session].filter(Boolean).join(' · ')));
  if (it.detail) c.append(el('p', 'detail', it.detail));
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
  c.append(note);
  return c;
}

// why: an unchanged item keeps its card, so a reply being typed survives a refresh.
function render(items, device) {
  const keep = new Map();
  const want = items.map((it) => {
    const k = [it.pane, it.at, it.risk, it.advice].join('|');
    const c = cards.get(k) || card(it);
    keep.set(k, c);
    return c;
  });
  cards.clear();
  keep.forEach((c, k) => cards.set(k, c));
  if (!want.length) want.push(el('div', 'empty', 'Nothing needs you.'));
  const same = want.length === list.children.length && want.every((c, i) => list.children[i] === c);
  if (!same) list.replaceChildren(...want);
  document.title = items.length ? '(' + items.length + ') pitwall' : 'pitwall';
  status.textContent = (device ? device + ' · ' : '') + new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

async function refresh() {
  if (!token) return;
  try {
    const r = await api('/api/items');
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

async function start() {
  const code = new URLSearchParams(location.hash.slice(1)).get('pair');
  if (code) {
    history.replaceState(null, '', location.pathname);
    try {
      const r = await fetch('/api/pair', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code }) });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      saveToken((await r.json()).token);
    } catch (e) {
      unpaired(e.message);
      return;
    }
  }
  if (!token) {
    unpaired();
    return;
  }
  schedule(0);
}

start();
