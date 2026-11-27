'use strict';
// invariant: daemon and agent text goes in with textContent or text nodes, never as HTML.

const KEY = 'pitwall-token';
const $ = (id) => document.getElementById(id);
const home = $('home'), chat = $('chat'), list = $('list'), push = $('push'), full = $('full');
const log = $('log'), entriesEl = $('entries'), pend = $('pend'), work = $('work'), more = $('more');
const asks = $('asks'), composer = $('composer'), text = $('text'), cnote = $('cnote'), queueList = $('queue');
const newpill = $('newpill'), pick = $('pick'), sheet = $('sheet');
const screenV = $('screen'), newV = $('newtab'), tsheet = $('tsheet');

let token = '';
try { token = localStorage.getItem(KEY) || ''; } catch (e) { /* private mode: pair again each visit */ }
let authed = !!token;
let pushInit = false;

function authHeaders(extra) {
  const h = Object.assign({}, extra);
  if (token) h['Authorization'] = 'Bearer ' + token;
  return h;
}

let tabs = [];          // the latest list, in sidebar order
let hello = { models: [], efforts: [] };
let pane = '';          // the open chat's pane, '' on the list
let model = '';         // the open chat's model, from its transcript
let first = 0, hasMore = false, loadingOlder = false;
const shown = new Map(); // seq: element of the open chat
let attachment = null, imageSending = false;
let pending = [];        // messages sent from here, not yet in the transcript
let stick = true;        // the log was at its bottom
let ctrl = null, backoff = 1000, retry = 0;
const cards = new Map(); // a request card by its key, so typing survives a refresh

const agents = { claude: 'Claude Code', codex: 'Codex', antigravity: 'Antigravity', pi: 'pi', omp: 'omp', gemini: 'Gemini', opencode: 'OpenCode', cursor: 'Cursor' };
const agentName = (a) => agents[a] || a || '';

function el(tag, cls, txt) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (txt !== undefined) e.textContent = txt;
  return e;
}

function saveToken(t) {
  token = t;
  try { t ? localStorage.setItem(KEY, t) : localStorage.removeItem(KEY); } catch (e) { /* kept for this visit */ }
}

// ---- Pairing.

function denied(msg) {
  authed = false;
  if (ctrl) ctrl.abort();
  ctrl = null;
  showList();
  full.hidden = true;
  $('newbtn').hidden = true;
  offline(false);
  push.replaceChildren();
  $('device').textContent = '';
  const box = el('div', 'empty');
  box.append(el('p', '', msg || 'Access denied.'));
  list.replaceChildren(box);
  document.title = 'pitwall';
}

function unpaired(msg) {
  authed = false;
  saveToken('');
  if (ctrl) ctrl.abort();
  ctrl = null;
  showList();
  full.hidden = true;
  $('newbtn').hidden = true;
  offline(false);
  push.replaceChildren();
  $('device').textContent = '';
  const box = el('div', 'empty');
  const how = el('p', '', 'On your computer, run ');
  how.append(el('code', '', 'pitwall remote pair'), ' or open Settings, Phone, and scan the code it shows, or enter its code here.');
  // why: an app added to the Home Screen keeps its own storage, so it pairs on its own, and it cannot scan.
  const form = el('form', 'inline');
  const input = el('input', 'field');
  input.placeholder = 'Pairing code';
  input.setAttribute('aria-label', input.placeholder);
  input.autocapitalize = 'characters';
  input.autocomplete = 'off';
  const go = el('button', 'btn primary', 'Pair');
  go.style.flex = 'none';
  form.append(input, go);
  form.onsubmit = (e) => { e.preventDefault(); pair(input.value); };
  msg = msg ? msg[0].toUpperCase() + msg.slice(1) : 'This phone is not paired.';
  box.append(el('p', '', msg), how, form);
  list.replaceChildren(box);
  document.title = 'pitwall';
}

async function pair(code) {
  code = code.toUpperCase().replace(/[^A-Z2-7]/g, '');
  try {
    const r = await fetch('/api/pair', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code }) });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    saveToken((await r.json()).token);
  } catch (e) {
    // why: a pairing link opened again must not drop the token it already gave; a dead one still gets 401 below
    if (token) { connect(); return; }
    unpaired(e.message);
    return;
  }
  connect();
  setupPush();
}

async function api(path, body) {
  const r = await fetch(path, {
    method: body ? 'POST' : 'GET',
    headers: authHeaders({ 'Content-Type': 'application/json' }),
    body: body ? JSON.stringify(body) : undefined,
    cache: 'no-store',
  });
  if (r.status === 401) {
    unpaired(token ? 'This phone is no longer paired.' : '');
    throw new Error('unpaired');
  }
  if (r.status === 403) {
    const msg = (await r.text()).trim() || 'Access denied.';
    denied(msg);
    throw new Error(msg);
  }
  if (!r.ok) throw new Error(failure(r.status, (await r.text()).trim()) || r.statusText);
  return r.json();
}

// failure is a refused request's words; net/http's own 404 is a route the
// daemon is too old to serve.
function failure(status, msg) {
  if (status === 404 && msg === '404 page not found') return 'Update pitwall on this computer to use this.';
  return msg;
}

// ---- The stream: Server-Sent Events read through fetch, which carries the token.

function offline(on) {
  document.querySelectorAll('.offline').forEach((o) => { o.hidden = !on; });
}

// connect opens the one stream, for the open chat's pane if any.
function connect() {
  clearTimeout(retry);
  if (ctrl) ctrl.abort();
  const my = new AbortController();
  ctrl = my;
  (async () => {
    try {
      const r = await fetch('/api/stream' + (pane ? '?pane=' + encodeURIComponent(pane) : ''), {
        headers: authHeaders(), cache: 'no-store', signal: my.signal,
      });
      if (r.status === 401) { unpaired(token ? 'This phone is no longer paired.' : ''); return; }
      if (r.status === 403) { denied((await r.text()).trim() || 'Access denied.'); return; }
      if (!r.ok || !r.body) throw new Error(r.statusText);
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf('\n\n')) >= 0) {
          event(buf.slice(0, i));
          buf = buf.slice(i + 2);
        }
      }
    } catch (e) { /* aborted or dropped: below */ }
    if (my !== ctrl || my.signal.aborted) return;
    offline(true);
    retry = setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 15000);
  })();
}

function event(block) {
  let name = 'message', data = '';
  for (const line of block.split('\n')) {
    if (line.startsWith('event: ')) name = line.slice(7);
    else if (line.startsWith('data: ')) data += line.slice(6);
  }
  if (!data) return;
  let v;
  try { v = JSON.parse(data); } catch (e) { return; }
  switch (name) {
    case 'hello':
      hello = v;
      authed = true;
      backoff = 1000;
      offline(false);
      full.hidden = !v.web;
      $('newbtn').hidden = !v.start;
      $('device').textContent = v.device || '';
      if (!pushInit) {
        pushInit = true;
        setupPush();
      }
      break;
    case 'tabs':
      tabs = v || [];
      renderList();
      if (pane) renderChatState();
      if (!screenV.hidden) screenTab();
      break;
    case 'reset':
      reset(v);
      break;
    case 'entries':
      if (v.model) model = v.model;
      add(v.entries || []);
      break;
    case 'commands':
      commands = v.commands || [];
      slashUpdate();
      break;
  }
}

// why: a phone drops the connection while the page sleeps, sometimes without an error.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && (token || authed)) {
    backoff = 1000;
    connect();
  }
});

// ---- The list, as the sidebar orders it.

const asking = ['pending-approval', 'awaiting-input', 'plan-ready', 'error'];

function pillOf(t) {
  if (t.label) return el('span', 'pill ' + t.state, t.label);
  return el('span', 'pill', t.send ? 'Idle' : 'Shell');
}

function renderList() {
  const out = [];
  let session = null, group = null;
  for (const t of tabs) {
    if (t.session !== session) {
      session = t.session;
      group = null;
      if (session) out.push(el('div', 'session', session));
    }
    if ((t.group || '') !== (group || '')) {
      group = t.group || '';
      if (group) out.push(el('div', 'group', group));
    }
    const row = el('button', 'row' + (asking.includes(t.state) ? ' needs' : ''));
    const l1 = el('div', 'line1');
    l1.append(pillOf(t), el('span', 'title', t.tab || 'Tab'));
    const waiting = (t.queue || []).length || t.queued;
    if (waiting) l1.append(el('span', 'badge', waiting + (t.queueHold ? ' held' : ' queued')));
    row.append(l1, el('div', 'meta', [agentName(t.agent), t.branch || t.repo].filter(Boolean).join(' · ') || 'No agent'));
    if (t.detail && t.state !== 'working') row.append(el('div', 'ask', t.detail));
    row.onclick = () => openChat(t.pane, true);
    out.push(row);
  }
  if (!tabs.length) out.push(el('div', 'empty', 'No tabs are open.'));
  list.replaceChildren(...out);
  const n = tabs.filter((t) => asking.includes(t.state)).length;
  document.title = n ? '(' + n + ') pitwall' : 'pitwall';
}

// viewBeat tells the computer this page shows the open chat (on), which
// it repeats every 30 s while the page is in view, or that it no longer
// does: the chat closed, the page went to the background or the screen
// went off. Only a page in view counts as watching for Since you left.
// keepalive lets the last one leave with a closing page.
function viewBeat(on) {
  if (!pane || !authed) return;
  fetch('/api/view', { method: 'POST', keepalive: true, cache: 'no-store', headers: authHeaders({ 'Content-Type': 'application/json' }), body: JSON.stringify({ pane, on }) }).catch(() => {});
}
setInterval(() => { if (document.visibilityState === 'visible') viewBeat(true); }, 30000);
document.addEventListener('visibilitychange', () => viewBeat(document.visibilityState === 'visible'));
window.addEventListener('pagehide', () => viewBeat(false));

// ---- Views and history: Back goes from a chat to the list.

function showList() {
  viewBeat(false);
  pane = '';
  chat.hidden = true;
  screenV.hidden = true;
  newV.hidden = true;
  home.hidden = false;
  closeSheet();
  closeTabSheet();
}

function openChat(p, push) {
  if (!p) return;
  if (push) history.pushState({ pane: p }, '', '#pane=' + encodeURIComponent(p));
  if (p !== pane) { clearAttachment(); viewBeat(false); }
  pane = p;
  viewBeat(document.visibilityState === 'visible');
  home.hidden = true;
  newV.hidden = true;
  screenV.hidden = true;
  chat.hidden = false;
  restarting = '';
  stopErr = '';
  shown.clear();
  entriesEl.replaceChildren();
  pend.replaceChildren();
  pending = [];
  cards.clear();
  asks.replaceChildren();
  more.textContent = '';
  model = '';
  commands = null;
  first = 0;
  hasMore = false;
  stick = true;
  newpill.hidden = true;
  text.value = '';
  grow();
  renderChatState();
  connect();
}

$('back').onclick = () => {
  if (history.state && history.state.pane) history.back();
  else { showList(); connect(); }
};
window.addEventListener('popstate', (e) => {
  const s = e.state || {};
  const p = s.pane;
  closeTabSheet();
  if (p && s.screen) {
    if (p !== pane || chat.hidden && screenV.hidden) openChat(p, false);
    showScreen(false);
  } else if (p) {
    if (p === pane && !screenV.hidden) hideScreen();
    else openChat(p, false);
  } else if (s.new) {
    showNew(false);
  } else if (pane || !newV.hidden) { showList(); connect(); }
});

function tabOf(p) { return tabs.find((t) => t.pane === p); }

// ---- The chat.

function atBottom() { return log.scrollHeight - log.scrollTop - log.clientHeight < 48; }
function toBottom() { log.scrollTop = log.scrollHeight; newpill.hidden = true; stick = true; }
log.addEventListener('scroll', () => {
  stick = atBottom();
  if (stick) newpill.hidden = true;
  if (pane) awayScroll(tabOf(pane));
  if (log.scrollTop < 300) older();
}, { passive: true });
newpill.onclick = toBottom;

// changing runs f, then keeps the log at its bottom if it was there.
function changing(f) {
  const was = stick || atBottom();
  f();
  if (was) toBottom();
  return was;
}

function reset(v) {
  shown.clear();
  entriesEl.replaceChildren();
  first = 0;
  hasMore = !!v.more;
  if (v.model) model = v.model;
  if (v.none) entriesEl.append(el('p', 'note', 'No transcript to show. pitwall shows the state of a shell or of an agent without hooks, never its screen.'));
  if (v.gone) entriesEl.append(el('p', 'note', 'This tab closed.'));
  add(v.entries || [], true);
  toBottom();
  more.textContent = hasMore ? 'Scroll up for earlier messages' : '';
}

function add(es, quiet) {
  let fresh = false;
  const was = changing(() => {
    for (const e of es) {
      const old = shown.get(e.seq);
      const node = entryEl(e);
      if (old) {
        if (old.open) node.open = true;
        old.replaceWith(node);
      } else {
        entriesEl.append(node);
        fresh = true;
        if (!first || e.seq < first) first = e.seq;
      }
      shown.set(e.seq, node);
      if (e.kind === 'user') settle(e.text, e.seq);
    }
  });
  if (fresh && !quiet && !was) newpill.hidden = false;
  renderChatState();
}

async function older() {
  if (!hasMore || loadingOlder || !pane || !first) return;
  loadingOlder = true;
  more.textContent = 'Loading earlier messages';
  const p = pane;
  try {
    const r = await api('/api/chat?pane=' + encodeURIComponent(p) + '&before=' + first);
    if (p !== pane) return;
    const h = log.scrollHeight;
    const frag = document.createDocumentFragment();
    for (const e of r.entries || []) {
      if (shown.has(e.seq)) continue;
      const node = entryEl(e);
      shown.set(e.seq, node);
      frag.append(node);
      if (e.seq < first) first = e.seq;
    }
    entriesEl.prepend(frag);
    log.scrollTop += log.scrollHeight - h; // why: what was on screen stays put
    hasMore = !!r.more;
  } catch (e) {
    hasMore = false;
  } finally {
    loadingOlder = false;
    if (p === pane) more.textContent = hasMore ? 'Scroll up for earlier messages' : '';
  }
}

function entryEl(e) {
  switch (e.kind) {
    case 'user': return el('div', 'msg user', e.text);
    case 'assistant': {
      const d = el('div', 'msg assistant');
      d.append(md(e.text || ''));
      return d;
    }
    case 'error': return el('div', 'msg error', e.text);
    case 'tool': {
      const d = el('details', 'tool' + (e.failed ? ' failed' : ''));
      const s = el('summary');
      s.append(el('span', 'tname', e.tool), el('span', 'targ', e.arg || ''));
      if (!e.done) s.append(el('i', 'dot'));
      else if (e.failed) s.append(el('span', 'tstate', 'failed'));
      d.append(s);
      if (e.arg) d.append(el('pre', 'out', e.arg));
      d.append(e.out ? el('pre', 'out', e.out) : el('p', 'note', e.done ? 'No output.' : 'Running.'));
      return d;
    }
  }
  return el('div', 'note', '');
}

// md renders paragraphs, headings, fenced code, lists, inline code, bold and http(s) links.
function md(src) {
  const frag = document.createDocumentFragment();
  const lines = src.replace(/\r\n?/g, '\n').split('\n');
  const fenceRe = /^\s*(`{3,}|~{3,})/;
  const itemRe = /^\s*([-*+]|\d+[.)])\s+/;
  const headRe = /^\s*#{1,6}\s+/;
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const fence = line.match(fenceRe);
    if (fence) {
      const buf = [];
      for (i++; i < lines.length && !lines[i].trim().startsWith(fence[1]); i++) buf.push(lines[i]);
      i++;
      const pre = el('pre', 'code');
      pre.append(el('code', '', buf.join('\n')));
      frag.append(pre);
    } else if (!line.trim()) {
      i++;
    } else if (headRe.test(line)) {
      const p = el('p', 'h');
      inline(p, line.replace(headRe, ''));
      frag.append(p);
      i++;
    } else if (itemRe.test(line)) {
      const l = el(/^\s*\d/.test(line) ? 'ol' : 'ul');
      while (i < lines.length && itemRe.test(lines[i])) {
        const li = el('li');
        inline(li, lines[i].replace(itemRe, ''));
        for (i++; i < lines.length && /^\s{2,}\S/.test(lines[i]) && !itemRe.test(lines[i]); i++) {
          li.append(' ');
          inline(li, lines[i].trim());
        }
        l.append(li);
      }
      frag.append(l);
    } else {
      const p = el('p');
      for (let k = 0; i < lines.length && lines[i].trim() && !fenceRe.test(lines[i]) && !itemRe.test(lines[i]) && !headRe.test(lines[i]); i++, k++) {
        if (k) p.append(el('br'));
        inline(p, lines[i]);
      }
      frag.append(p);
    }
  }
  return frag;
}

function inline(parent, s) {
  const re = /`([^`]+)`|\*\*([^*]+)\*\*|\[([^\]]+)\]\(([^)\s]+)\)|(https?:\/\/[^\s<>()]*[^\s<>().,;:!?'"])/g;
  let last = 0, m;
  while ((m = re.exec(s))) {
    if (m.index > last) parent.append(s.slice(last, m.index));
    if (m[1] !== undefined) parent.append(el('code', '', m[1]));
    else if (m[2] !== undefined) parent.append(el('strong', '', m[2]));
    else link(parent, m[3] !== undefined ? m[3] : m[5], m[4] !== undefined ? m[4] : m[5]);
    last = re.lastIndex;
  }
  if (last < s.length) parent.append(s.slice(last));
}

function link(parent, label, href) {
  let u = null;
  try { u = new URL(href); } catch (e) { /* not a URL: text */ }
  if (!u || (u.protocol !== 'https:' && u.protocol !== 'http:')) {
    parent.append(label === href ? label : label + ' (' + href + ')');
    return;
  }
  const a = el('a', '', label);
  a.href = u.href;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  parent.append(a);
}

// renderChatState draws the open tab's header, working row, request card and composer.
function renderChatState() {
  const t = tabOf(pane);
  $('ctitle').textContent = t ? t.tab : 'Tab';
  const meta = $('cmeta');
  meta.replaceChildren();
  if (t) meta.append(pillOf(t), [agentName(t.agent), t.picker ? '' : prettyModel(model)].filter(Boolean).join(' · '));
  pick.hidden = !(t && t.picker);
  pick.textContent = prettyModel(model) || 'Model';
  $('scrbtn').hidden = !(t && !t.chat && hello.screen);
  $('tmenu').hidden = !(t && (hello.close || hello.restart && t.exited));
  const running = !!t && (t.state === 'working' || t.state === 'connecting');
  work.hidden = !running;
  if (running) {
    let what = t.detail || '';
    const calls = [...shown.values()].filter((n) => n.matches('details.tool'));
    const last = calls[calls.length - 1];
    if (!what && last && last.querySelector('.dot')) what = last.querySelector('.tname').textContent + ' ' + last.querySelector('.targ').textContent;
    work.lastChild.textContent = 'Working' + (what.trim() ? ': ' + what.trim() : '');
  }
  renderAsks(t);
  renderAway(t);
  renderHold(t);
  renderQueue(t);
  composer.hidden = !(t && t.send);
  $('stop').hidden = !(t && t.send && t.state === 'working' && hello.stop);
  $("attach").hidden = !(t && t.attach && hello.upload);
  cnote.hidden = !!(t && t.send);
  if (t && t.send && stopErr) {
    if (t.state === 'working') { cnote.hidden = false; cnote.textContent = 'Stop: ' + stopErr; }
    else stopErr = ''; // the turn ended
  }
  if (!t) cnote.textContent = 'This tab is not open now.';
  else if (t.exited && hello.restart) exitedBand(t);
  else if (!t.send) cnote.textContent = t.exited ? t.label + '. Open on desktop to restart the agent.' : 'This tab runs no agent, so it takes no messages from here.';
  if (t && !t.exited) restarting = '';
  text.placeholder = !t ? '' : answerCard ? 'Pick an answer above, or type your own' : running || t.state === 'pending-approval' ? 'Queue a message' : t.state === 'awaiting-input' ? 'Answer' : 'Message ' + agentName(t.agent);
  updatePending(t);
}

const modelAliases = {
  default: 'Default',
  fable: 'Fable 5.2',
  opus: 'Opus 5.5',
  sonnet: 'Sonnet 5.5',
  haiku: 'Haiku 5.5',
};

function shortModel(m) { return (m || '').replace(/^claude-/i, ''); }

// Versioned ids carry their own name; only moving aliases need a table.
function prettyModel(m) {
  let id = m || '';
  const wide = /\[1m\]$/i.test(id) ? ' 1M' : '';
  id = id.replace(/\[1m\]$/i, '');
  const alias = id.toLowerCase();
  if (Object.hasOwn(modelAliases, alias)) return modelAliases[alias] + wide;
  const short = shortModel(id);
  const parts = /^([a-z]+)-([0-9]+)(?:[.-]([0-9]+))?(?:-([a-z0-9-]+))?$/.exec(short.toLowerCase());
  if (!parts) return short + wide;
  const version = parts[2] + (parts[3] && parts[3].length !== 8 ? '.' + parts[3] : '');
  if (parts[1] === 'gpt') {
    const words = (parts[4] || '').split('-').filter(Boolean).map((w) => w[0].toUpperCase() + w.slice(1));
    return 'GPT-' + version + (words.length ? ' ' + words.join(' ') : '') + wide;
  }
  return parts[1][0].toUpperCase() + parts[1].slice(1) + ' ' + version + wide;
}

// restarting is the pane whose Restart is on its way; restartErr why the last failed.
let restarting = '', restartErr = '';
// stopErr is why the last Stop failed; it shows until the next Stop or the turn's end.
let stopErr = '';

// exitedBand is the exited tab's line with Restart, as the menu's.
function exitedBand(t) {
  const b = el('button', 'btn primary', restarting === t.pane ? 'Restarting…' : 'Restart');
  b.disabled = restarting === t.pane;
  b.onclick = () => restartTab(t);
  cnote.replaceChildren(el('span', '', t.label + '.'), b);
  if (restartErr) cnote.append(el('span', 'qerr', restartErr));
}

function restartTab(t) {
  if (restarting) return;
  restarting = t.pane;
  restartErr = '';
  renderChatState();
  api('/api/restart', { pane: t.pane }).catch((e) => {
    restarting = '';
    if (e.message !== 'unpaired') restartErr = e.message;
    if (pane === t.pane) renderChatState();
  });
}

// renderHold says a menu in the agent's terminal holds the queue, with
// Open screen to answer it.
let holdShown = '';
function renderHold(t) {
  const on = !!t && t.holdMenu && !t.chat;
  const k = on ? t.pane + '|' + t.hold + '|' + !!hello.screen : '';
  if (k === holdShown) return;
  holdShown = k;
  const out = [];
  if (on) {
    const r = el('div', 'holdrow');
    r.append(el('p', 'note', 'Waiting: ' + (t.hold || 'a menu is open in the agent\'s terminal')));
    if (hello.screen) r.append(screenButton());
    out.push(r);
  }
  changing(() => $('hold').replaceChildren(...out));
}

function screenButton() {
  const b = el('button', 'btn', 'Open screen');
  b.onclick = () => showScreen(true);
  return b;
}

// renderQueue lists what the open tab's chat queued, why it waits, and
// each message's Send now and Remove.
let queueShown = '';
function renderQueue(t) {
  const rows = (t && t.queue) || [];
  const k = JSON.stringify([t && t.pane, t && t.queueHold, rows]);
  if (k === queueShown) return;
  queueShown = k;
  const out = [];
  if (rows.length) out.push(el('p', 'qhead', [rows.length + ' queued', t.queueHold].filter(Boolean).join(' · ')));
  for (const q of rows) {
    const body = el('div', 'qtext');
    body.append(el('span', 'qline', q.text.trim().split('\n')[0] || 'Image'));
    if (q.error) body.append(el('span', 'qerr', 'Not sent: ' + q.error));
    const now = el('button', 'btn quiet', q.error ? 'Retry' : 'Send now');
    const rm = el('button', 'btn quiet', 'Remove');
    const act = (action) => {
      now.disabled = rm.disabled = true;
      api('/api/queue', { pane: t.pane, id: q.id, action }).catch((e) => {
        now.disabled = rm.disabled = false;
        if (e.message !== 'unpaired') body.append(el('span', 'qerr', e.message));
      });
    };
    now.onclick = () => act('now');
    rm.onclick = () => act('remove');
    const r = el('div', 'qrow');
    r.append(body, now, rm);
    out.push(r);
  }
  changing(() => queueList.replaceChildren(...out));
}

// answerCard is the shown card whose open question the box answers, null
// while the box sends messages.
let answerCard = null;

// boxAnswers is whether the box's text body answers the shown card: never
// a draft from before the card, a slash command or a picture.
function boxAnswers(body, image) {
  const b = body.trim();
  return !!answerCard && !image && !!b && body !== answerCard.draft && !/^\/[^\s/]+(\s|$)/.test(b);
}

function renderAsks(t) {
  const want = [];
  if (t && (t.question || t.answer || (t.requests && t.requests.length) || ['pending-approval', 'plan-ready', 'awaiting-input'].includes(t.state))) {
    const k = [t.pane, t.at, t.allowBlocked, t.question && t.question.id, (t.requests || []).map((r) => r.id)].join('|');
    const c = cards.get(k) || askCard(t);
    if (c.syncRequests && t.requests) c.syncRequests(t.requests);
    cards.clear();
    cards.set(k, c);
    want.push(c);
  } else {
    cards.clear();
  }
  answerCard = want[0] && want[0].boxAnswer ? want[0] : null;
  const same = want.length === asks.children.length && want.every((c, i) => asks.children[i] === c);
  if (!same) changing(() => asks.replaceChildren(...want));
}

// renderAway draws the open tab's Since you left card (tab.away): a
// marker in the log above the first missed entry, and the card above the
// box only when what was missed is taller than the log's view, so it never
// repeats what is on screen. The card says how long the phone and the
// window had the chat out of sight, the summary, what the log shows, and
// has a jump to the first missed entry and Dismiss.
let awayKey = '', awayCardEl = null, awayTall = false, awayHid = '';
const awayMarkEl = el('div', 'away-mark');
const awayID = (a) => a.chat + '@' + a.at;
function renderAway(t) {
  const box = $('away'), a = t && t.away && awayID(t.away) !== awayHid ? t.away : null;
  const k = a ? awayID(a) + '@' + a.asking : '';
  if (k !== awayKey) {
    awayKey = k;
    awayCardEl = a ? awayCard(t, a) : null;
    awayTall = false;
  }
  placeAwayMark(a);
  if (a && atBottom()) awayTall = !awayMarkEl.isConnected || awayMarkEl.getBoundingClientRect().top < log.getBoundingClientRect().top;
  const want = a && awayTall ? [awayCardEl] : [];
  if (want.length !== box.children.length || want[0] !== box.firstChild) changing(() => box.replaceChildren(...want));
}

// awayScroll follows the reader through what they missed: scrolled up
// to the marker, then back down to the bottom, they have read it, and the
// card goes here and everywhere, as in the app. At the bottom it also
// decides again whether the card shows.
let awayRead = '';
function awayScroll(t) {
  const a = t && t.away;
  if (!a || awayID(a) === awayHid || !awayMarkEl.isConnected) return;
  if (!atBottom()) {
    if (awayMarkEl.getBoundingClientRect().top >= log.getBoundingClientRect().top - 1) awayRead = awayKey;
    return;
  }
  if (awayRead === awayKey) {
    awayDismiss(t);
    return;
  }
  renderAway(t);
}

// awayDismiss closes the card and the marker here, until the tab list
// drops the card, and on the computer, which clears it everywhere.
function awayDismiss(t) {
  awayHid = awayID(t.away);
  awayRead = '';
  $('away').replaceChildren();
  awayMarkEl.remove();
  api('/api/away', { pane: t.pane }).catch(() => {});
}

// awayFor is how long the card's absence was, as "2h 14m".
function awayFor(a) {
  const mins = Math.max(1, Math.round((Date.parse(a.at) - Date.parse(a.since)) / 60000));
  return mins < 60 ? mins + 'm' : mins < 1440 ? Math.floor(mins / 60) + 'h' + (mins % 60 ? ' ' + (mins % 60) + 'm' : '') : Math.floor(mins / 1440) + 'd';
}

// placeAwayMark puts the log's marker above the first missed entry, or
// takes it out with no card or with that entry not loaded.
function placeAwayMark(a) {
  const seq = a && [...shown.keys()].filter((q) => q >= a.seq).sort((p, q) => p - q)[0];
  const node = seq !== undefined && shown.get(seq);
  if (!node || seq !== a.seq && seq === Math.min(...shown.keys())) { awayMarkEl.remove(); return; }
  awayMarkEl.textContent = 'Since you left · ' + awayFor(a);
  if (node.previousSibling !== awayMarkEl) changing(() => node.before(awayMarkEl));
}

function awayCard(t, a) {
  const now = { working: 'Working', connecting: 'Working', 'pending-approval': 'Needs you', 'awaiting-input': 'Needs you', 'plan-ready': 'Needs you', error: 'Needs you', completed: 'Done', 'pr-open': 'Done', parked: 'Done' }[t.state];
  const c = el('div', 'card away');
  const head = el('div', 'label');
  const x = el('button', 'btn quiet', '✕');
  x.type = 'button';
  x.setAttribute('aria-label', 'Dismiss');
  x.onclick = () => awayDismiss(t);
  head.append(el('span', '', ['Since you left', awayFor(a), now].filter(Boolean).join(' · ')), x);
  c.append(head);
  if (a.status) c.append(el('p', 'detail', a.status));
  if (a.asking) c.append(el('p', 'facts', 'Summarizing…'));
  else if (a.needs) c.append(el('p', 'detail', a.needs));
  const n = (v, one) => v + ' ' + one + (v === 1 ? '' : 's');
  const facts = [];
  if (a.replies) facts.push(n(a.replies, 'message') + (a.turns > 1 ? ' in ' + n(a.turns, 'turn') : ''));
  if (a.editedN) facts.push(n(a.editedN, 'file') + ' edited');
  if (a.commands) facts.push(n(a.commands, 'command'));
  if (a.tests) facts.push('tests ' + a.tests);
  if (facts.length) c.append(el('p', 'facts', facts.join(' · ')));
  if (a.failed) c.append(el('p', 'facts', 'Last failed: ' + a.failed));
  const jump = el('button', 'btn quiet jump', 'Show what you missed');
  jump.type = 'button';
  jump.onclick = () => {
    const node = awayMarkEl.isConnected ? awayMarkEl : shown.get(Math.min(...shown.keys()));
    if (node) node.scrollIntoView({ block: 'start' });
  };
  c.append(jump);
  return c;
}

function askCard(t) {
  const c = el('div', 'card');
  c.draft = text.value; // a draft from before the question is a message, not its answer
  const note = el('p', 'note');
  const done = (msg) => { note.className = 'note'; note.textContent = msg; };
  const failed = (e) => { if (e.message !== 'unpaired') { note.className = 'note bad'; note.textContent = e.message; } };
  if (t.question) {
    const q = t.question;
    c.append(el('div', 'label', 'Question'));
    if (q.tabs && q.tabs.length) {
      const bar = el('div', 'step-chips');
      q.tabs.forEach((tab, i) => {
        const cls = 'step-chip' + (i === q.index ? ' current' : tab.answered ? ' done' : '');
        bar.append(el('span', cls, (tab.answered ? '✓ ' : '') + tab.label));
      });
      c.append(bar);
    } else if (q.total > 1) {
      c.append(el('div', 'step-meta', `Question ${(q.index || 0) + 1} of ${q.total}`));
    }
    c.append(el('p', 'detail', q.prompt));
    const buttons = [];
    const answer = (option, text = '') => {
      buttons.forEach((b) => { b.disabled = true; });
      api('/api/question', { pane: t.pane, id: q.id, option, text })
        .catch((e) => { buttons.forEach((b) => { b.disabled = false; }); failed(e); });
    };
    q.options.forEach((o, i) => {
      const b = el('button', 'btn question-option' + (o.selected ? ' primary' : ''), (q.multi ? (o.selected ? '✓ ' : '□ ') : '') + o.label);
      if (q.multi) b.setAttribute('aria-pressed', String(o.selected));
      b.onclick = () => answer(i);
      buttons.push(b); c.append(b);
    });
    if (q.other) {
      const other = el('textarea', 'field');
      other.placeholder = 'Other'; other.setAttribute('aria-label', 'Other'); other.rows = 1;
      const send = el('button', 'btn', 'Send Other');
      send.onclick = () => { if (other.value.trim()) answer(-2, other.value); };
      buttons.push(send); c.append(other, send);
      c.boxAnswer = (body) => { answer(-2, body.trim()); return true; };
    }
    if (q.submit) {
      const submit = el('button', 'btn primary', 'Submit'); submit.onclick = () => answer(-1);
      buttons.push(submit); c.append(submit);
    }
    if (q.canBack || q.canNext) {
      const nav = el('div', 'step-nav');
      if (q.canBack) {
        const back = el('button', 'btn', 'Back'); back.onclick = () => answer(-3);
        buttons.push(back); nav.append(back);
      }
      if (q.canNext) {
        const next = el('button', 'btn', 'Next'); next.onclick = () => answer(-4);
        buttons.push(next); nav.append(next);
      }
      c.append(nav);
    }
  } else if (t.requests && t.requests.length) {
    const boxes = t.requests.map((r) => request(t.pane, r, done, failed));
    c.append(...boxes);
    c.syncRequests = (reqs) => {
      reqs.forEach((r, i) => { if (boxes[i] && boxes[i].syncRequest) boxes[i].syncRequest(r); });
    };
    if (t.requests.every((r) => r.kind === 'input')) c.boxAnswer = boxes[boxes.length - 1].boxAnswer;
  } else {
    c.append(el('div', 'label', t.label || 'Waiting'));
    if (t.detail) c.append(el('p', 'detail', t.detail));
    if (t.answer) {
      const row = el('div', 'opts');
      const allow = el('button', 'btn primary', t.allowBlocked ? 'Open on desktop' : 'Allow');
      const deny = el('button', 'btn', 'Deny');
      const send = (yes) => {
        allow.disabled = deny.disabled = true;
        api('/api/answer', { pane: t.pane, at: t.at, allow: yes })
          .then(() => done(yes ? 'Allowed.' : 'Denied.'))
          .catch((e) => { allow.disabled = deny.disabled = false; failed(e); });
      };
      allow.onclick = () => t.allowBlocked ? api('/api/open', { pane: t.pane }).then(() => done('Opened on desktop.')).catch(failed) : send(true);
      if (t.allowBlocked) c.append(el('p', 'note', t.allowBlocked));
      deny.onclick = () => send(false);
      row.append(allow, deny);
      c.append(row);
    } else if (t.state === 'awaiting-input' && t.reply) {
      done('Type your answer below.');
    } else if (t.state === 'pending-approval') {
      done('Answer it in its tab.');
    } else if (t.state === 'plan-ready') {
      done('Answer the plan in its tab or from the sidebar.');
    } else if (hello.screen && !t.chat) {
      done('Its screen shows a menu: answer it with the keys on its screen.');
      c.append(screenButton());
    } else {
      done('Its screen shows a menu: answer it in its tab.');
    }
  }
  c.append(note);
  return c;
}

// request is a chat tab's pending approval, plan or question, answered by its id.
function request(chatID, r, done, failed) {
  const box = el('div', 'request');
  box.append(el('div', 'label', r.kind === 'plan' ? 'Plan' : r.kind === 'input' ? 'Question' : 'Approval'));
  if (r.tool || r.command) box.append(el('pre', 'command', [r.tool, r.command].filter(Boolean).join(': ')));
  if (r.path) box.append(el('div', 'meta', r.path));
  if (r.detail) box.append(el('p', 'detail', r.detail));
  const buttons = [];
  const lock = (on) => buttons.forEach((b) => { b.disabled = on; });
  if (r.kind === 'input') {
    const qs = r.questions || [];
    const picks = qs.map((q) => {
      const t = el('textarea', 'field');
      t.rows = 1;
      t.placeholder = q.options && q.options.length ? 'Or type an answer' : 'Answer';
      t.setAttribute('aria-label', t.placeholder);
      const init = (r.answered && r.answered[q.id]) || [];
      return { id: q.id, multi: !!q.multiSelect, labels: [...init], text: t };
    });
    const answered = (p) => p.labels.length > 0 || !!p.text.value.trim();
    const nextStep = (i) => {
      for (let j = i + 1; j < picks.length; j++) if (!answered(picks[j])) return j;
      return Math.min(i + 1, Math.max(0, picks.length - 1));
    };
    let step = r.step != null ? Math.min(Math.max(r.step, 0), Math.max(0, qs.length - 1)) : 0;
    let sent = false;
    const area = el('div');
    const submit = () => {
      if (sent) return;
      if (!picks.every(answered)) { failed(new Error('Answer every question.')); return; }
      const answers = {};
      for (const p of picks) {
        const v = p.text.value.trim();
        answers[p.id] = v ? (p.multi ? [...p.labels, v] : [v]) : [...p.labels];
      }
      sent = true;
      lock(true);
      const shown = el('p', 'detail', 'Answered: ' + picks.map((p) => answers[p.id].join(', ')).join(', '));
      area.replaceChildren(shown); // at once, before the agent takes it
      api('/api/input', { chat: chatID, request: r.id, answers })
        .then(() => done(''))
        .catch((e) => { sent = false; renderStep(); lock(false); failed(e); });
    };
    const renderStep = () => {
      if (sent || !qs.length) return;
      buttons.length = 0;
      const nodes = [];
      if (qs.length > 1) {
        const bar = el('div', 'step-chips');
        qs.forEach((q, i) => {
          const isDone = answered(picks[i]);
          const lbl = q.header || (i === step ? `Question ${i + 1} of ${qs.length}` : `Question ${i + 1}`);
          const chip = el('button', 'step-chip' + (i === step ? ' current' : isDone ? ' done' : ''), (isDone ? '✓ ' : '') + lbl);
          chip.type = 'button';
          chip.onclick = () => { step = i; done(''); renderStep(); };
          buttons.push(chip);
          bar.append(chip);
        });
        nodes.push(bar);
      }
      const q = qs[step];
      const pk = picks[step];
      nodes.push(el('p', 'detail', q.prompt || q.header || ''));
      if (q.options && q.options.length) {
        const row = el('div', 'opts');
        let preview = '';
        q.options.forEach((o) => {
          const on = pk.labels.includes(o.label);
          if (o.preview && (!preview || on)) preview = o.preview;
          const b = el('button', 'btn' + (on ? ' primary' : ''), (pk.multi ? (on ? '✓ ' : '□ ') : '') + o.label);
          if (o.description) b.title = o.description;
          if (pk.multi) b.setAttribute('aria-pressed', String(on));
          b.onclick = () => {
            if (pk.multi) {
              pk.labels = pk.labels.includes(o.label) ? pk.labels.filter((l) => l !== o.label) : [...pk.labels, o.label];
              renderStep();
              return;
            }
            pk.labels = [o.label];
            pk.text.value = ''; // the card shows what it sends
            if (step === qs.length - 1 && picks.every(answered)) { submit(); return; }
            if (step < qs.length - 1) { step = nextStep(step); renderStep(); return; }
            const openIdx = picks.findIndex((x) => !answered(x));
            if (openIdx >= 0) step = openIdx;
            renderStep();
          };
          buttons.push(b);
          row.append(b);
        });
        nodes.push(row);
        if (preview) nodes.push(el('pre', 'command', preview.replace(/\n+$/, '')));
      }
      const typed = el('div', 'inline');
      typed.append(pk.text);
      nodes.push(typed);
      const nav = el('div', 'step-nav');
      if (qs.length > 1 && step > 0) {
        const back = el('button', 'btn', 'Back');
        back.onclick = () => { step--; done(''); renderStep(); };
        buttons.push(back);
        nav.append(back);
      }
      if (qs.length > 1 && step < qs.length - 1) {
        const next = el('button', 'btn primary', 'Next');
        next.onclick = () => {
          if (!answered(pk)) { failed(new Error('Pick or type an answer.')); return; }
          done('');
          step = nextStep(step);
          renderStep();
        };
        buttons.push(next);
        nav.append(next);
      } else {
        const send = el('button', 'btn primary', 'Send');
        send.onclick = submit;
        buttons.push(send);
        nav.append(send);
      }
      nodes.push(nav);
      area.replaceChildren(...nodes);
    };
    renderStep();
    box.append(area);
    // boxAnswer takes the box's text as the current question's answer.
    box.boxAnswer = (body) => {
      if (sent || !picks.length) return false;
      const pk = picks[step] || picks.find((x) => !answered(x)) || picks[picks.length - 1];
      pk.text.value = body.trim();
      if (picks.every(answered)) { submit(); return true; }
      step = nextStep(step);
      renderStep();
      return true;
    };
    let tuiStep = r.step != null ? r.step : -1;
    box.syncRequest = (next) => {
      if (sent || !next || next.step == null) return;
      const s = Math.min(Math.max(next.step, 0), Math.max(0, qs.length - 1));
      let changed = false;
      if (s !== tuiStep) { tuiStep = s; step = s; changed = true; }
      if (next.answered) {
        qs.forEach((q, i) => {
          const vals = next.answered[q.id];
          if (vals && vals.length && !answered(picks[i])) {
            picks[i].labels = [...vals];
            changed = true;
          }
        });
      }
      if (changed) renderStep();
    };
    return box;
  }
  const reason = el('textarea', 'field');
  reason.rows = 1;
  reason.placeholder = 'Reason to deny (optional)';
  reason.setAttribute('aria-label', reason.placeholder);
  const answer = (decision, msg) => {
    lock(true);
    api('/api/approve', { chat: chatID, request: r.id, decision, reason: decision === 'deny' ? reason.value.trim() : '' })
      .then(() => done(msg))
      .catch((e) => { lock(false); failed(e); });
  };
  const row = el('div', 'opts');
  const allow = el('button', 'btn primary', r.kind === 'plan' ? 'Approve plan' : 'Allow');
  allow.onclick = () => answer('allow', 'Allowed.');
  buttons.push(allow);
  row.append(allow);
  if (r.kind !== 'plan' && !r.noSession) { // noSession: the prompt has no such answer
    const session = el('button', 'btn', 'Allow for session');
    session.onclick = () => answer('allow-session', 'Allowed for this session.');
    buttons.push(session);
    row.append(session);
  }
  const deny = el('button', 'btn', r.kind === 'plan' ? 'Keep planning' : 'Deny');
  deny.onclick = () => answer('deny', 'Denied.');
  buttons.push(deny);
  row.append(deny);
  const why = el('div', 'inline');
  why.append(reason);
  box.append(row, why);
  return box;
}

// ---- Sending: a message shows as queued, then sent, until the transcript has it.

// grow runs at each change of the box's text, so the / menu follows it.
function grow() {
  text.style.height = 'auto';
  text.style.height = Math.min(text.scrollHeight + 2, 160) + 'px';
  slashUpdate();
}
text.addEventListener('input', grow);
text.addEventListener('click', () => slashUpdate());
text.addEventListener('keyup', (e) => { if (!['ArrowUp', 'ArrowDown'].includes(e.key) || !slash.rows.length) slashUpdate(); });
text.addEventListener('keydown', (e) => {
  if (slashKey(e)) return;
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); composer.requestSubmit(); }
});

// ---- The / menu: the chat's commands that match the /word at the caret,
// as the window's composer lists them. The functions down to slashPick port
// internal/ui/chatin's Slash, SlashMatches, MidCommands and SlashMenu's
// update, mark and pick; chatin's TestWebSlashPort runs them against the Go.
// As in the Android app, Codex gets no menu past the draft's first word.

let commands = null;          // the open chat's, from its "commands" event; an older daemon sends none, and no menu opens
const slashUsed = new Map();  // the commands picked here, by name, which rank first
const slash = { rows: [], start: 0, sel: 0, query: '', picked: '', shut: '' };
const slashEl = $('slash');

// slashWord is the /word the caret is in: its index and the whole word. The
// / starts the text or follows whitespace, so and/or and a URL are none.
function slashWord(s, caret) {
  if (caret < 1 || caret > s.length) return null;
  let start = caret;
  while (start > 0 && !/\s/.test(s[start - 1])) start--;
  if (start === caret || s[start] !== '/') return null;
  let end = caret;
  while (end < s.length && !/\s/.test(s[end])) end++;
  return { start, word: s.slice(start, end) };
}

const utf8Len = (s) => new TextEncoder().encode(s).length; // Go's len, which the three-letter rules count

function subsequence(s, q) {
  for (const ch of q) {
    const i = s.indexOf(ch);
    if (i < 0) return false;
    s = s.slice(i + ch.length);
  }
  return true;
}

// slashTier is how well c matches q, best 0, -1 for not: the name or an alias
// that is q, starts with it, has a word (after - _ : or a space) that does,
// has it inside, the name with its letters in order, then the description.
function slashTier(c, q) {
  let best = -1;
  [c.name, ...(c.aliases || [])].forEach((n, i) => {
    n = n.toLowerCase();
    let t = -1;
    if (n === q) t = 0;
    else if (n.startsWith(q)) t = 1;
    else if (n.split(/[-_: ]/).some((w) => w !== '' && w.startsWith(q))) t = 2;
    else if (n.includes(q)) t = 3;
    else if (i === 0 && utf8Len(q) >= 3 && subsequence(n, q)) t = 4;
    if (t >= 0 && (best < 0 || t < best)) best = t;
  });
  if (best < 0 && utf8Len(q) >= 3 && (c.description || '').toLowerCase().includes(q)) best = 5;
  return best;
}

// slashMatches is cmds that match query, best first; within a tier the more
// used first, then the list's order.
function slashMatches(cmds, query, used) {
  const q = query.toLowerCase();
  return cmds.map((c) => ({ c, t: slashTier(c, q) })).filter((m) => m.t >= 0)
    .sort((a, b) => a.t - b.t || (used.get(b.c.name) || 0) - (used.get(a.c.name) || 0))
    .map((m) => m.c);
}

// midPrompt is whether provider p reads a /skill past the prompt's first word.
function midPrompt(p) { return !p || p === 'claude' || p === 'antigravity'; }

// midCommands is the skills and custom commands, never a built-in.
function midCommands(cmds) { return cmds.filter((c) => !c.terminal && ['user', 'project', 'plugin', 'system'].includes(c.source)); }

// slashRows is the menu for text with the caret at caret in a chat of
// provider p: the word's index and its rows, none for a path.
function slashRows(text, caret, p, cmds, used) {
  const w = slashWord(text, caret);
  const q = w ? w.word.slice(1) : '';
  if (!w || /[/\\]/.test(q)) return { start: 0, rows: [] };
  if (w.start > 0) {
    if (!midPrompt(p)) return { start: 0, rows: [] };
    cmds = midCommands(cmds);
  }
  return { start: w.start, rows: slashMatches(cmds, q, used) };
}

// slashMark is what a pick of c writes before its name in a chat of
// provider p: $ for a Codex skill, which is how Codex reads one, else /.
function slashMark(c, p) { return p === 'codex' && c.source ? '$' : '/'; }

// slashPick is text with the word at start, and the one space after it, as
// mark, name and a space, and the caret after that space.
function slashPick(text, start, name, mark = '/') {
  start = Math.min(start, text.length);
  let end = start;
  while (end < text.length && !/\s/.test(text[end])) end++;
  if (text[end] === ' ') end++;
  const put = mark + name + ' ';
  return { text: text.slice(0, start) + put + text.slice(end), caret: start + put.length };
}

// slashNote is the quiet note at a row's right, as chatin.RowNote.
function slashNote(c) {
  if (c.terminal) return 'opens in the terminal';
  return { user: 'Personal', project: 'Project', plugin: 'Plugin', system: 'System' }[c.source] || '';
}

// slashUpdate opens, narrows or closes the menu for the box's /word. A new
// query highlights the first row; the same one keeps its row.
function slashUpdate() {
  const t = text.value;
  if (t !== slash.shut) slash.shut = '';
  let r = { start: 0, rows: [] };
  if (commands && !slash.shut && pane && text.selectionStart === text.selectionEnd) {
    r = slashRows(t, text.selectionEnd, slashAgent(), commands, slashUsed);
  }
  const w = slashWord(t, text.selectionEnd);
  const q = w ? w.word.slice(1) : '';
  slash.start = r.start;
  slash.rows = r.rows;
  if (q !== slash.query) slash.sel = 0;
  else if (slash.picked) slash.sel = Math.max(0, r.rows.findIndex((c) => c.name === slash.picked));
  slash.query = q;
  slash.sel = Math.min(slash.sel, Math.max(0, r.rows.length - 1));
  slash.picked = r.rows.length ? r.rows[slash.sel].name : '';
  slashDraw();
}

function slashDraw() {
  slashEl.hidden = !slash.rows.length;
  text.setAttribute('aria-expanded', String(!!slash.rows.length));
  slashEl.replaceChildren(...slash.rows.map((c, i) => {
    const b = el('button', 'srow' + (i === slash.sel ? ' on' : ''));
    b.type = 'button';
    b.setAttribute('role', 'option');
    b.setAttribute('aria-selected', String(i === slash.sel));
    const name = el('span', 'sname', slashMark(c, slashAgent()) + c.name);
    if (c.hint) name.append(el('span', 'shint', c.hint));
    const words = el('span', 'stext');
    words.append(name);
    if (c.description) words.append(el('span', 'sdesc', c.description));
    b.append(words);
    const note = slashNote(c);
    if (note) b.append(el('span', 'snote', note));
    b.addEventListener('mousedown', (e) => e.preventDefault()); // why: the box keeps the focus, so the keyboard stays up
    b.onclick = () => slashChoose(c);
    return b;
  }));
  const on = slashEl.children[slash.sel];
  if (on) on.scrollIntoView({ block: 'nearest' });
}

function slashAgent() { const t = tabOf(pane); return t ? t.agent : ''; }

function slashChoose(c) {
  const r = slashPick(text.value, slash.start, c.name, slashMark(c, slashAgent()));
  slashUsed.set(c.name, (slashUsed.get(c.name) || 0) + 1);
  text.value = r.text;
  text.focus();
  text.setSelectionRange(r.caret, r.caret);
  grow();
}

// slashKey runs a key on the open menu: Up and Down move the highlight,
// Enter or Tab fills the command in, Esc closes it until the text changes.
function slashKey(e) {
  const n = slash.rows.length;
  if (!n || e.isComposing || e.metaKey || e.ctrlKey || e.altKey) return false;
  switch (e.key) {
    case 'ArrowUp': slash.sel = (slash.sel + n - 1) % n; break;
    case 'ArrowDown': slash.sel = (slash.sel + 1) % n; break;
    case 'Escape': slash.shut = text.value; slash.rows = []; break;
    case 'Enter': case 'Tab':
      if (e.shiftKey) return false;
      e.preventDefault();
      slashChoose(slash.rows[slash.sel]);
      return true;
    default: return false;
  }
  e.preventDefault();
  slash.picked = n && slash.rows.length ? slash.rows[slash.sel].name : '';
  slashDraw();
  return true;
}

composer.onsubmit = (e) => {
  e.preventDefault();
  const body = text.value;
  const image = attachment, target = pane;
  if (imageSending || (!body.trim() && !image) || !target) return;
  if (boxAnswers(body, image) && answerCard.boxAnswer(body)) {
    text.value = '';
    grow();
    return;
  }
  if (image) { imageSending = true; $("send").disabled = true; }
  const p = { text: body, image: !!image, after: Math.max(0, ...shown.keys()), node: el('div', 'msg user pending', body || (image ? 'Picture' : '')) };
  p.label = el('span', 'state', 'Sending');
  p.node.append(p.label);
  pending.push(p);
  pend.append(p.node);
  toBottom();
  text.value = '';
  grow();
  const t = tabOf(pane);
  (image ? uploadImage(target, image.file) : Promise.resolve(''))
    .then((image) => api('/api/send', { pane: target, text: body, image }))
    .then((r) => {
      if (attachment === image) clearAttachment();
      p.id = r.id;
      p.sent = true;
      p.queued = !!t && (t.queued > 0 || !['', 'completed', 'error', 'pr-open', 'awaiting-input'].includes(t.state || ''));
      p.at = Date.now();
      updatePending(tabOf(pane));
    })
    .catch((err) => {
      if (err.message === 'unpaired') return;
      p.node.classList.add('failed');
      p.label.textContent = 'Not sent: ' + err.message;
      p.failed = true;
    }).finally(() => { imageSending = false; $("send").disabled = false; });
};

function norm(s) { return (s || '').replace(/\s+/g, ' ').trim(); }

// settle drops the pending message the transcript now shows.
function settle(t, seq) {
  const i = pending.findIndex((p) => seq > p.after && norm(p.image ? p.text.replace(/\[Image #\d+\]/g, "") : p.text) === norm(p.image ? t.replace(/\[Image #\d+\]/g, "") : t));
  if (i >= 0) {
    pending[i].node.remove();
    pending.splice(i, 1);
  }
}

function updatePending(t) {
  for (const p of pending) {
    if (!p.sent || p.failed) continue;
    if (t && (t.dropped || []).includes(p.id)) {
      p.node.classList.add('failed');
      p.label.textContent = 'Not sent: the agent exited';
      p.failed = true;
      continue;
    }
    if (p.queued && !(t && t.queued > 0)) p.queued = false;
    p.label.textContent = p.queued ? t && t.hold ? 'waiting: ' + t.hold : 'Queued' : 'Sent';
    // A tab without a transcript never shows the message; let it go after a while.
    if (!p.queued && !shown.size && Date.now() - p.at > 8000) { p.node.remove(); p.gone = true; }
  }
  pending = pending.filter((p) => !p.gone);
}

// ---- Model and effort, typed as /model and /effort.

function choices(box, what, values) {
  box.replaceChildren(...values.map((v) => {
    const b = el('button', 'btn' + (what === 'model' && prettyModel(model) === prettyModel(v) ? ' on' : ''), what === 'model' ? prettyModel(v) : v);
    b.onclick = () => {
      const note = $('mnote');
      const all = sheet.querySelectorAll('.choices .btn');
      // why: one pick at a time, so two commands never reach Claude Code at once.
      all.forEach((x) => { x.disabled = true; });
      note.className = 'note';
      note.textContent = 'Sending /' + what + ' ' + v;
      api('/api/command', { pane, what, value: v })
        .then(() => { note.textContent = 'Sent /' + what + ' ' + v + '.'; })
        .catch((e) => { if (e.message !== 'unpaired') { note.className = 'note bad'; note.textContent = e.message; } })
        .finally(() => all.forEach((x) => { x.disabled = false; }));
    };
    return b;
  }));
}

pick.onclick = () => {
  choices($('models'), 'model', hello.models || []);
  choices($('efforts'), 'effort', hello.efforts || []);
  $('mnote').textContent = 'Claude Code takes these at its input box, between turns.';
  sheet.hidden = false;
  $('mclose').focus();
};
function closeSheet() { sheet.hidden = true; }
$('mclose').onclick = closeSheet;
sheet.addEventListener('click', (e) => { if (e.target === sheet) closeSheet(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !sheet.hidden) closeSheet(); });

// ---- The keyboard: the app fills the visual viewport, so the composer stays above it.

function fit() {
  const vv = window.visualViewport;
  const h = vv ? vv.height : window.innerHeight;
  const root = document.documentElement;
  root.style.setProperty('--vvh', h + 'px');
  root.style.setProperty('--vvt', (vv ? vv.offsetTop : 0) + 'px');
  document.body.classList.toggle('kb', h < window.innerHeight - 120);
  if (stick && pane) log.scrollTop = log.scrollHeight;
}
if (window.visualViewport) {
  window.visualViewport.addEventListener('resize', fit);
  window.visualViewport.addEventListener('scroll', fit);
}
window.addEventListener('resize', fit);
fit();

// ---- Notifications and the full client.

// why: the device token stays in this page; the full client gets a session cookie of its own.
full.querySelector('button').onclick = async () => {
  try {
    location.assign((await api('/api/web', {})).url);
  } catch (e) { /* unpaired, or the bundle is gone */ }
};

function note(msg, button) {
  const p = el('p', 'note', msg);
  if (button) p.append(' ', button);
  push.replaceChildren(p);
}

function keyBytes(k) {
  const s = atob(k.replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(s, (ch) => ch.charCodeAt(0));
}

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
  const on = el('button', 'btn primary', 'Turn on');
  on.style.flex = 'none';
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
    const p = (e.data && e.data.pane) || '';
    if (p && p !== pane) openChat(p, true);
  });
}

function start() {
  const hash = new URLSearchParams(location.hash.slice(1));
  const code = hash.get('pair');
  const p = hash.get('pane') || '';
  history.replaceState(null, '', location.pathname);
  if (code) { pair(code); return; }
  // why: try connecting even without a token so Tailscale-authenticated phones open directly;
  // an unpaired loopback connection gets 401 and falls back to the pairing form.
  if (p) openChat(p, true);
  else connect();
}

start();

function clearAttachment() {
  if (attachment) URL.revokeObjectURL(attachment.url);
  attachment = null;
  $('attachment').replaceChildren(); $('attachment').hidden = true; $('photo').value = '';
}
function attachImage(file) {
  if (!file) return;
  if (file.size > 20 * 1024 * 1024) { cnote.hidden = false; cnote.textContent = 'The image is over 20 MiB.'; return; }
  clearAttachment();
  attachment = { file, url: URL.createObjectURL(file) };
  const img = el('img'); img.src = encodeURI(attachment.url); img.alt = 'Attached picture'; img.width = img.height = 48;
  const remove = el('button', 'btn', 'Remove picture'); remove.onclick = clearAttachment;
  $('attachment').append(img, remove); $('attachment').hidden = false;
}
async function uploadImage(pane, file) {
  const r = await fetch('/api/upload?pane=' + encodeURIComponent(pane), { method: 'POST', headers: authHeaders(), body: file });
  if (!r.ok) throw new Error(await r.text());
  return (await r.json()).image;
}
$('attach').onclick = () => $('photo').click();
$('photo').onchange = () => attachImage($('photo').files[0]);
text.addEventListener('paste', (e) => {
  if ($('attach').hidden) return;
  const file = Array.from(e.clipboardData.files).find((f) => f.type.startsWith('image/'));
  if (file) { e.preventDefault(); attachImage(file); }
});

// ---- Stop: interrupts the agent's turn, as the window's Stop does.

$('stop').onclick = () => {
  const b = $('stop'), p = pane;
  b.disabled = true;
  stopErr = '';
  renderChatState();
  api('/api/stop', { pane: p })
    .catch((e) => { if (e.message !== 'unpaired' && p === pane) { stopErr = e.message; renderChatState(); } })
    .finally(() => { b.disabled = false; });
};

// ---- The tab's actions: Restart an exited tab, Close tab after a second tap.

$('tmenu').onclick = () => {
  const t = tabOf(pane);
  if (!t) return;
  const acts = [];
  if (t.exited && hello.restart) {
    const r = el('button', 'btn', 'Restart');
    r.onclick = () => { closeTabSheet(); restartTab(t); };
    acts.push(r);
  }
  if (hello.close) {
    const c = el('button', 'btn', 'Close tab');
    let armed = false;
    c.onclick = () => {
      if (!armed) {
        armed = true;
        c.classList.add('armed');
        c.textContent = 'Tap again to close';
        return;
      }
      c.disabled = true;
      api('/api/close', { tab: t.id }).then(() => {
        closeTabSheet();
        if (pane === t.pane) $('back').onclick();
      }).catch((e) => {
        c.disabled = false;
        if (e.message !== 'unpaired') { $('tnote').hidden = false; $('tnote').className = 'note bad'; $('tnote').textContent = e.message; }
      });
    };
    acts.push(c);
  }
  $('tacts').replaceChildren(...acts);
  $('tnote').hidden = true;
  tsheet.hidden = false;
  $('tclose').focus();
};
function closeTabSheet() { tsheet.hidden = true; }
$('tclose').onclick = closeTabSheet;
tsheet.addEventListener('click', (e) => { if (e.target === tsheet) closeTabSheet(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !tsheet.hidden) closeTabSheet(); });

// ---- The Screen view: the tab's screen as text, read each second while
// it shows, and keys that press into the tab, for a menu pitwall cannot answer.

const screenKeys = [
  [['esc', 'Esc'], ['tab', 'Tab'], ['shift+tab', 'Shift+Tab'], ['ctrl+c', 'Ctrl+C'], ['enter', 'Enter']],
  [['left', '←'], ['up', '↑'], ['down', '↓'], ['right', '→']],
  [1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => [String(n), String(n)]),
];
let screenTimer = 0, screenRead = 0, screenName = '';

// screenTab keeps the view's title, and leaves for the list with a note
// once its tab is gone from the list: it closed, or its pane went away.
function screenTab() {
  const t = tabOf(pane);
  if (t) {
    screenName = t.tab || 'Tab';
    $('stitle').textContent = screenName;
    return true;
  }
  clearTimeout(screenTimer);
  screenRead++;
  history.replaceState(null, '', location.pathname);
  showList();
  connect();
  listNote((screenName || 'The tab') + ' closed.');
  return false;
}

// listNote shows msg over the list for a few seconds.
let listNoteTimer = 0;
function listNote(msg) {
  const n = $('lnote');
  n.textContent = msg;
  n.hidden = false;
  clearTimeout(listNoteTimer);
  listNoteTimer = setTimeout(() => { n.hidden = true; }, 6000);
}

$('keys').replaceChildren(...screenKeys.map((row) => {
  const r = el('div', 'krow');
  r.append(...row.map(([key, label]) => {
    const b = el('button', 'btn', label);
    b.type = 'button';
    b.setAttribute('aria-label', key);
    b.onclick = () => pressKey(key);
    return b;
  }));
  return r;
}));

function showScreen(push) {
  if (!pane || !hello.screen) return;
  if (push) history.pushState({ pane, screen: 1 }, '', '#pane=' + encodeURIComponent(pane) + '&screen=1');
  closeTabSheet();
  chat.hidden = true;
  screenV.hidden = false;
  screenName = '';
  if (!screenTab()) return;
  $('stext').textContent = 'Reading the screen';
  $('snote').hidden = true;
  $('keys').hidden = !hello.key;
  readScreen();
}

function hideScreen() {
  clearTimeout(screenTimer);
  screenV.hidden = true;
  chat.hidden = !pane;
}

$('sback').onclick = () => {
  if (history.state && history.state.screen) history.back();
  else hideScreen();
};

function screenNote(msg) {
  $('snote').hidden = !msg;
  $('snote').textContent = msg || '';
}

// readScreen reads the screen and the next one a second after; a tab in the
// background gets none.
function readScreen() {
  clearTimeout(screenTimer);
  if (screenV.hidden || !pane || !screenTab()) return;
  const p = pane, n = ++screenRead;
  api('/api/screen?pane=' + encodeURIComponent(p)).then((r) => {
    if (n !== screenRead || p !== pane) return;
    const pre = $('stext');
    const cols = Math.max(1, ...(r.text || '').split('\n').map((l) => l.length));
    const w = $('sbox').clientWidth - 24;
    pre.style.fontSize = Math.min(13, Math.max(9, w / (cols * 0.6))) + 'px';
    pre.textContent = r.text || '';
    screenNote('');
  }).catch((e) => {
    if (e.message !== 'unpaired' && n === screenRead) screenNote(e.message);
  }).finally(() => {
    if (n === screenRead && !screenV.hidden && !document.hidden) screenTimer = setTimeout(readScreen, 1000);
  });
}
document.addEventListener('visibilitychange', () => { if (!document.hidden && !screenV.hidden) readScreen(); });

function pressKey(key) {
  const p = pane;
  api('/api/key', { pane: p, key }).then(() => {
    clearTimeout(screenTimer);
    screenTimer = setTimeout(readScreen, 150);
  }).catch((e) => { if (e.message !== 'unpaired') screenNote(e.message); });
}

$('scrbtn').onclick = () => showScreen(true);

// ---- New tab: a folder, an agent, a worktree or none, and a first message.

let newOpts = null, newGen = 0;

// shortDir is path with ~ for home, cut in the middle to n characters so its
// end, which tells folders apart, stays; as the app's shortDir.
function shortDir(path, home, n) {
  if (home && home !== '/') {
    if (path === home) path = '~';
    else if (path.startsWith(home) && '/\\'.includes(path[home.length])) path = '~' + path.slice(home.length);
  }
  const r = [...path];
  if (n < 8 || r.length <= n) return path;
  const head = Math.floor((n - 1) / 3);
  return r.slice(0, head).join('') + '…' + r.slice(r.length - (n - 1 - head)).join('');
}

function option(value, label) {
  const o = el('option', '', label);
  o.value = value;
  return o;
}

function showNew(push) {
  if (!hello.start) return;
  if (push) history.pushState({ new: 1 }, '', '#new');
  home.hidden = true;
  chat.hidden = true;
  screenV.hidden = true;
  newV.hidden = false;
  $('nnote').hidden = true;
  $('nstart').disabled = false;
  $('nstart').textContent = 'Start';
  const sessions = [...new Set(tabs.map((t) => t.session).filter(Boolean))];
  const sel = $('nsession');
  const keep = sel.value;
  sel.replaceChildren(...sessions.map((s) => option(s, s)));
  if (sessions.includes(keep)) sel.value = keep;
  $('nsessionl').hidden = sessions.length < 2;
  loadNew();
}

function loadNew() {
  const n = ++newGen;
  $('nload').hidden = false;
  $('nload').className = 'note';
  $('nload').textContent = 'Loading…';
  $('nform').hidden = true;
  api('/api/new?session=' + encodeURIComponent($('nsession').value || '')).then((o) => {
    if (n !== newGen) return;
    newOpts = o;
    const w = Math.max(16, Math.floor(($('nform').parentElement.clientWidth - 60) / 8.5));
    $('nfolder').replaceChildren(...(o.folders || []).map((f, i) => option(String(i), f.name + ' · ' + shortDir(f.path, o.home, Math.max(8, w - f.name.length - 3)))),
      option('typed', 'Type a path'));
    $('nagent').replaceChildren(...(o.agents || []).map((a) => option(a.id, a.name)));
    $('nload').hidden = true;
    $('nform').hidden = false;
    newFields();
  }).catch((e) => {
    if (n !== newGen || e.message === 'unpaired') return;
    $('nload').className = 'note bad';
    $('nload').textContent = e.message;
    const again = el('button', 'btn', 'Try again');
    again.onclick = loadNew;
    $('nload').append(' ', again);
  });
}

// newFields shows the fields the picks call for: the path for a typed
// folder, the branch for a new worktree, no first message for a terminal.
function newFields() {
  const typed = $('nfolder').value === 'typed';
  const f = !typed && newOpts.folders[+$('nfolder').value];
  $('npath').hidden = !typed;
  $('nworktreel').hidden = !(typed || f && f.git);
  if ($('nworktreel').hidden) $('nworktree').value = '';
  $('nbranch').hidden = $('nworktree').value !== 'new';
  $('nbriefl').hidden = $('nagent').value === 'terminal';
}
$('nfolder').onchange = () => { newFields(); if ($('nfolder').value === 'typed') $('npath').focus(); };
$('nworktree').onchange = () => { newFields(); if ($('nworktree').value === 'new') $('nbranch').focus(); };
$('nagent').onchange = newFields;
$('nsession').onchange = loadNew;

$('nform').onsubmit = (e) => {
  e.preventDefault();
  const note = $('nnote');
  const typed = $('nfolder').value === 'typed';
  const dir = typed ? $('npath').value.trim() : (newOpts.folders[+$('nfolder').value] || {}).path;
  const req = { session: $('nsession').value || '', dir, agent: $('nagent').value };
  if (!$('nworktreel').hidden && $('nworktree').value === 'new') {
    req.worktree = true;
    req.branch = $('nbranch').value.trim();
  }
  if ($('nagent').value !== 'terminal') req.brief = $('nbrief').value.trim();
  const missing = !dir ? 'Type the folder\'s path.' : !req.agent ? 'This computer offers no agent.' : req.worktree && !req.branch ? 'Name the new worktree\'s branch.' : '';
  note.hidden = !missing;
  note.textContent = missing;
  if (missing) return;
  $('nstart').disabled = true;
  $('nstart').textContent = 'Starting…';
  const n = newGen;
  api('/api/new', req).then((r) => {
    if (n !== newGen || newV.hidden) return;
    $('nbrief').value = '';
    $('nbranch').value = '';
    history.replaceState({ pane: r.pane }, '', '#pane=' + encodeURIComponent(r.pane));
    openChat(r.pane, false);
  }).catch((e) => {
    $('nstart').disabled = false;
    $('nstart').textContent = 'Start';
    if (e.message !== 'unpaired') { note.hidden = false; note.textContent = e.message; }
  });
};

$('newbtn').onclick = () => showNew(true);
$('nback').onclick = () => {
  if (history.state && history.state.new) history.back();
  else { showList(); connect(); }
};
