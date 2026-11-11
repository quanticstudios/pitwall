'use strict';
// invariant: daemon and agent text goes in with textContent or text nodes, never as HTML.

const KEY = 'pitwall-token';
const $ = (id) => document.getElementById(id);
const home = $('home'), chat = $('chat'), list = $('list'), push = $('push'), full = $('full');
const log = $('log'), entriesEl = $('entries'), pend = $('pend'), work = $('work'), more = $('more');
const asks = $('asks'), composer = $('composer'), text = $('text'), cnote = $('cnote');
const newpill = $('newpill'), pick = $('pick'), sheet = $('sheet');

let token = '';
try { token = localStorage.getItem(KEY) || ''; } catch (e) { /* private mode: pair again each visit */ }

let tabs = [];          // the latest list, in sidebar order
let hello = { models: [], efforts: [] };
let pane = '';          // the open chat's pane, '' on the list
let model = '';         // the open chat's model, from its transcript
let first = 0, hasMore = false, loadingOlder = false;
const shown = new Map(); // seq: element of the open chat
let pending = [];        // messages sent from here, not yet in the transcript
let stick = true;        // the log was at its bottom
let ctrl = null, backoff = 1000, retry = 0;
const cards = new Map(); // a request card by its key, so typing survives a refresh

const agents = { claude: 'Claude Code', codex: 'Codex', pi: 'pi', omp: 'omp', gemini: 'Gemini', opencode: 'OpenCode', cursor: 'Cursor' };
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

function unpaired(msg) {
  saveToken('');
  if (ctrl) ctrl.abort();
  ctrl = null;
  showList();
  full.hidden = true;
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
  box.append(el('p', '', msg || 'This phone is not paired.'), how, form);
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
    unpaired(e.message);
    return;
  }
  connect();
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

// ---- The stream: Server-Sent Events read through fetch, which carries the token.

function offline(on) {
  document.querySelectorAll('.offline').forEach((o) => { o.hidden = !on; });
}

// connect opens the one stream, for the open chat's pane if any.
function connect() {
  if (!token) return;
  clearTimeout(retry);
  if (ctrl) ctrl.abort();
  const my = new AbortController();
  ctrl = my;
  (async () => {
    try {
      const r = await fetch('/api/stream' + (pane ? '?pane=' + encodeURIComponent(pane) : ''), {
        headers: { 'Authorization': 'Bearer ' + token }, cache: 'no-store', signal: my.signal,
      });
      if (r.status === 401) { unpaired('This phone is no longer paired.'); return; }
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
      backoff = 1000;
      offline(false);
      full.hidden = !v.web;
      $('device').textContent = v.device || '';
      break;
    case 'tabs':
      tabs = v || [];
      renderList();
      if (pane) renderChatState();
      break;
    case 'reset':
      reset(v);
      break;
    case 'entries':
      if (v.model) model = v.model;
      add(v.entries || []);
      break;
  }
}

// why: a phone drops the connection while the page sleeps, sometimes without an error.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && token) {
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
    if (t.queued) l1.append(el('span', 'badge', t.queued + ' queued'));
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

// ---- Views and history: Back goes from a chat to the list.

function showList() {
  pane = '';
  chat.hidden = true;
  home.hidden = false;
  closeSheet();
}

function openChat(p, push) {
  if (!p) return;
  if (push) history.pushState({ pane: p }, '', '#pane=' + encodeURIComponent(p));
  pane = p;
  home.hidden = true;
  chat.hidden = false;
  shown.clear();
  entriesEl.replaceChildren();
  pend.replaceChildren();
  pending = [];
  cards.clear();
  asks.replaceChildren();
  more.textContent = '';
  model = '';
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
  const p = e.state && e.state.pane;
  if (p) openChat(p, false);
  else if (pane) { showList(); connect(); }
});

function tabOf(p) { return tabs.find((t) => t.pane === p); }

// ---- The chat.

function atBottom() { return log.scrollHeight - log.scrollTop - log.clientHeight < 48; }
function toBottom() { log.scrollTop = log.scrollHeight; newpill.hidden = true; stick = true; }
log.addEventListener('scroll', () => {
  stick = atBottom();
  if (stick) newpill.hidden = true;
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
      if (e.kind === 'user') settle(e.text);
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
  if (t) meta.append(pillOf(t), [agentName(t.agent), prettyModel(model)].filter(Boolean).join(' · '));
  pick.hidden = !(t && t.picker);
  pick.textContent = prettyModel(model) || 'Model';
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
  composer.hidden = !(t && t.send);
  cnote.hidden = !!(t && t.send);
  if (!t) cnote.textContent = 'This tab is not open now.';
  else if (!t.send) cnote.textContent = 'This tab runs no agent, so it takes no messages from here.';
  text.placeholder = !t ? '' : running || t.state === 'pending-approval' ? 'Queue a message' : t.state === 'awaiting-input' ? 'Answer' : 'Message ' + agentName(t.agent);
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

function renderAsks(t) {
  const want = [];
  if (t && (t.answer || (t.requests && t.requests.length) || ['pending-approval', 'plan-ready', 'awaiting-input'].includes(t.state))) {
    const k = [t.pane, t.at, (t.requests || []).map((r) => r.id)].join('|');
    const c = cards.get(k) || askCard(t);
    cards.clear();
    cards.set(k, c);
    want.push(c);
  } else {
    cards.clear();
  }
  const same = want.length === asks.children.length && want.every((c, i) => asks.children[i] === c);
  if (!same) changing(() => asks.replaceChildren(...want));
}

function askCard(t) {
  const c = el('div', 'card');
  const note = el('p', 'note');
  const done = (msg) => { note.className = 'note'; note.textContent = msg; };
  const failed = (e) => { if (e.message !== 'unpaired') { note.className = 'note bad'; note.textContent = e.message; } };
  if (t.requests && t.requests.length) {
    t.requests.forEach((r) => c.append(request(t.pane, r, done, failed)));
  } else {
    c.append(el('div', 'label', t.label || 'Waiting'));
    if (t.detail) c.append(el('p', 'detail', t.detail));
    if (t.answer) {
      const row = el('div', 'opts');
      const allow = el('button', 'btn primary', 'Allow');
      const deny = el('button', 'btn', 'Deny');
      const send = (yes) => {
        allow.disabled = deny.disabled = true;
        api('/api/answer', { pane: t.pane, at: t.at, allow: yes })
          .then(() => done(yes ? 'Allowed.' : 'Denied.'))
          .catch((e) => { allow.disabled = deny.disabled = false; failed(e); });
      };
      allow.onclick = () => send(true);
      deny.onclick = () => send(false);
      row.append(allow, deny);
      c.append(row);
    } else if (t.state === 'awaiting-input' && t.reply) {
      done('Type your answer below.');
    } else if (t.state === 'pending-approval') {
      done('Answer it in its tab.');
    } else if (t.state === 'plan-ready') {
      done('Answer the plan in its tab or from the sidebar.');
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
    const picks = (r.questions || []).map((q) => {
      box.append(el('p', 'detail', q.prompt || q.header || ''));
      const pk = { id: q.id, label: '' };
      if (q.options && q.options.length) {
        const row = el('div', 'opts');
        q.options.forEach((o) => {
          const b = el('button', 'btn', o.label);
          if (o.description) b.title = o.description;
          b.onclick = () => { pk.label = o.label; row.querySelectorAll('button').forEach((x) => x.classList.toggle('primary', x === b)); };
          buttons.push(b);
          row.append(b);
        });
        box.append(row);
      }
      pk.text = el('textarea', 'field');
      pk.text.rows = 1;
      pk.text.placeholder = q.options && q.options.length ? 'Or type an answer' : 'Answer';
      pk.text.setAttribute('aria-label', pk.text.placeholder);
      const typed = el('div', 'inline');
      typed.append(pk.text);
      box.append(typed);
      return pk;
    });
    const send = el('button', 'btn primary', 'Send');
    buttons.push(send);
    send.onclick = () => {
      const answers = {};
      for (const p of picks) {
        const v = p.text.value.trim() || p.label;
        if (!v) { failed(new Error('Answer every question.')); return; }
        answers[p.id] = [v];
      }
      lock(true);
      api('/api/input', { chat: chatID, request: r.id, answers })
        .then(() => done('Sent.'))
        .catch((e) => { lock(false); failed(e); });
    };
    const row = el('div', 'opts');
    row.append(send);
    box.append(row);
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
  if (r.kind !== 'plan') {
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

function grow() {
  text.style.height = 'auto';
  text.style.height = Math.min(text.scrollHeight + 2, 160) + 'px';
}
text.addEventListener('input', grow);
text.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); composer.requestSubmit(); }
});

composer.onsubmit = (e) => {
  e.preventDefault();
  const body = text.value;
  if (!body.trim() || !pane) return;
  const p = { text: body, node: el('div', 'msg user pending', body) };
  p.label = el('span', 'state', 'Sending');
  p.node.append(p.label);
  pending.push(p);
  pend.append(p.node);
  toBottom();
  text.value = '';
  grow();
  const t = tabOf(pane);
  api('/api/send', { pane, text: body })
    .then((r) => {
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
    });
};

function norm(s) { return (s || '').replace(/\s+/g, ' ').trim(); }

// settle drops the pending message the transcript now shows.
function settle(t) {
  const i = pending.findIndex((p) => p.sent && norm(p.text) === norm(t));
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
    p.label.textContent = p.queued ? 'Queued' : 'Sent';
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
  if (!token) { unpaired(); return; }
  // why: a tapped notification opens its chat, and Back still reaches the list.
  if (p) openChat(p, true);
  else connect();
  setupPush();
}

start();
