(async () => {
  const out = {};
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  const q = (id, multi) => ({ id, prompt: 'Which?', multiSelect: multi, options: [{ id: 'r', label: 'Red' }, { id: 'b', label: 'Blue' }] });
  const feed = (requests) => event('event: tabs\ndata: ' + JSON.stringify([{ pane: 'c', tab: 'chat', agent: 'claude', state: 'awaiting-input', send: true, requests }]));
  const tap = (label, n = 0) => [...asks.querySelectorAll('button')].filter((b) => b.textContent === label)[n].click();
  const submit = async (value) => { text.value = value; composer.requestSubmit(); await wait(150); };
  try {
    await wait(200);
    feed([{ id: 'i1', kind: 'input', questions: [q('q', false)] }]);
    out.placeholder = text.placeholder;
    tap('Blue');
    out.shown = asks.textContent;
    await wait(150);
    [...asks.querySelectorAll('button')].filter((b) => b.textContent === 'Red').forEach((b) => b.click());
    await wait(150);
    feed([{ id: 'i2', kind: 'input', questions: [q('a', false), q('b', true)] }]);
    tap('Red', 0);
    tap('Blue', 1);
    await submit('Green');
    feed([]);
    text.value = 'a draft from before';
    feed([{ id: 'i3', kind: 'input', questions: [q('q', false)] }]);
    await submit('a draft from before');
    await submit('/compact');
    feed([{ id: 'a1', kind: 'approval', tool: 'Bash', command: 'ls' }, { id: 'i4', kind: 'input', questions: [q('q', false)] }]);
    await submit('both wait');
    out.bothPlaceholder = text.placeholder;
  } catch (e) {
    out.error = String((e && e.stack) || e);
  }
  await fetch('/result', { method: 'POST', body: JSON.stringify(out) });
})();
