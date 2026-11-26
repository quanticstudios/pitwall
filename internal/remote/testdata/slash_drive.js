(() => {
  const cmds = [
    { name: 'compact', description: 'Free up context by summarizing the conversation so far', hint: '[instructions]' },
    { name: 'config', description: 'Open settings', terminal: true },
    { name: 'review', description: 'Review a pull request' },
    { name: 'code-review', description: 'Review the diff for correctness bugs', source: 'user' },
    { name: 'release', description: 'Cut a release and tag it', source: 'project' },
    { name: 'pdf', description: 'Read and fill PDF forms', source: 'plugin' },
  ];
  const feed = (agent) => event('event: tabs\ndata: ' + JSON.stringify([{ pane: 'c', tab: 'skill menu', agent, state: 'completed', send: true }]));
  const type = (s, caret = s.length) => {
    text.focus();
    text.value = s;
    text.setSelectionRange(caret, caret);
    text.dispatchEvent(new Event('input'));
  };
  const rows = () => [...slashEl.querySelectorAll('.srow .sname')].map((n) => n.firstChild.textContent);
  const key = (k) => text.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
  const tap = (name) => {
    const b = [...slashEl.querySelectorAll('.srow')].find((r) => r.querySelector('.sname').firstChild.textContent === name);
    const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true });
    b.dispatchEvent(down);
    if (!down.defaultPrevented) throw new Error('a tap on a row would take the focus from the box');
    b.click();
  };
  feed('claude');
  if (shot) {
    event('event: commands\ndata: ' + JSON.stringify({ commands: cmds }));
    if (shot === 'start') type('/re');
    else type('please run /re and then push', 14);
    return;
  }
  const out = {};
  (async () => {
    const wait = (ms) => new Promise((r) => setTimeout(r, ms));
    try {
      await wait(200);
      type('/re');
      out.old = rows();
      event('event: commands\ndata: ' + JSON.stringify({ commands: cmds }));
      out.start = rows();
      out.hidden = slashEl.hidden;
      key('ArrowDown');
      out.sel = slashEl.querySelector('.srow.on .sname').firstChild.textContent;
      key('Enter');
      out.afterEnter = text.value;
      out.closed = slashEl.hidden;
      type('please run /re and then push', 14);
      out.mid = rows();
      tap('/code-review');
      out.afterTap = text.value;
      out.caret = text.selectionStart;
      out.focused = document.activeElement === text;
      type('use /com');
      out.builtinMid = rows();
      type('see /tmp/x');
      out.path = rows();
      type('/re');
      key('Escape');
      out.esc = slashEl.hidden;
      text.dispatchEvent(new KeyboardEvent('keyup', { key: 'ArrowLeft', bubbles: true }));
      out.escStays = slashEl.hidden;
      type('/rel');
      out.reopened = rows();
      feed('codex');
      type('run /re');
      out.codexMid = rows();
      type('/re');
      out.codexStart = rows().length;
      type('');
      out.empty = slashEl.hidden;
      type('/re');
      out.rowHeight = slashEl.querySelector('.srow').getBoundingClientRect().height;
    } catch (e) {
      out.error = String((e && e.stack) || e);
    }
    await fetch('/result', { method: 'POST', body: JSON.stringify(out) });
  })();
})();
