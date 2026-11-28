(async () => {
  const out = {};
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  // Drain queued scroll events across two frames, then wait on the real scroll event rather than a fixed timeout.
  const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  const scrollTo = (fn) => new Promise((r) => {
    log.addEventListener('scroll', () => r(), { once: true });
    fn();
  });
  const away = {
    chat: 'c', since: '2026-11-27T09:00:00Z', at: '2026-11-27T11:14:00Z', replies: 1, turns: 1, editedN: 2, edited: ['login.go', 'login_test.go'],
    commands: 2, failed: 'go test ./auth/...', tests: 'failed', item: 'e1', seq: 2,
    status: 'The cookie race is fixed; TestLoginRefresh still fails by one second.', needs: 'Pick: pad the expiry for clock skew, or check on the client.',
  };
  const feed = (a) => event('event: tabs\ndata: ' + JSON.stringify([{ pane: 'c', tab: 'Login cookie race', agent: 'claude', state: 'completed', label: 'Done', send: true, away: a }]));
  const card = () => document.getElementById('away');
  const mark = () => document.querySelector('#entries .away-mark');
  const entries = (n) => {
    const es = [{ seq: 1, kind: 'user', text: 'Make the login test stop racing the session cookie.' }, { seq: 2, kind: 'tool', tool: 'Edit', arg: 'auth/login.go', done: true }];
    for (let i = 3; i <= n; i++) es.push({ seq: i, kind: 'assistant', text: 'Checked step ' + i + ': the cookie expiry is set from the server clock, which runs a second ahead.' });
    event('event: reset\ndata: ' + JSON.stringify({ entries: es }));
  };
  try {
    entries(40);
    feed(away);
    out.text = card().textContent;
    out.tallMark = mark() ? mark().textContent : '';
    if (shot) return;
    await wait(200);
    feed(undefined);
    entries(3);
    feed({ ...away, at: '2026-11-27T11:15:00Z' });
    out.shortCard = card().children.length;
    out.shortMark = mark() ? mark().textContent : '';
    feed(undefined);
    entries(40);
    feed({ ...away, at: '2026-11-27T11:15:30Z' });
    log.scrollTop = log.scrollHeight;
    await frame();
    await scrollTo(() => mark().scrollIntoView({ block: 'start' }));
    out.readCardStays = card().children.length === 1;
    await scrollTo(() => { log.scrollTop = log.scrollHeight; });
    out.readThrough = card().children.length === 0 && !mark();
    await wait(100);
    feed(undefined);
    entries(40);
    feed({ ...away, at: '2026-11-27T11:16:00Z' });
    [...card().querySelectorAll('button')].find((b) => b.textContent === 'Show what you missed').click();
    [...card().querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === 'Dismiss').click();
    await wait(150);
    out.dismissed = card().children.length === 0 && !mark();
    feed(undefined);
    log.scrollTop = log.scrollHeight;
    feed({ ...away, at: '2026-11-27T12:00:00Z', status: '', needs: '', asking: true });
    out.asking = card().textContent.includes('Summarizing…');
    await wait(100);
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
    document.dispatchEvent(new Event('visibilitychange'));
    await wait(100);
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
    document.dispatchEvent(new Event('visibilitychange'));
    await wait(100);
  } catch (e) {
    out.error = String((e && e.stack) || e);
  }
  await fetch('/result', { method: 'POST', body: JSON.stringify(out) });
})();
