// Plays index.html's #steps in a browser, as TestWebSession does in Go.
"use strict";
const log = (line) => { document.getElementById("log").textContent += line + "\n"; };
const vars = { dir: new URLSearchParams(location.search).get("dir") || "" };
const fill = (v) => typeof v === "string" ? v.replace(/\$\{(\w+)\}/g, (_, k) => vars[k] ?? "")
  : Array.isArray(v) ? v.map(fill) : v && typeof v === "object" ? Object.fromEntries(Object.entries(v).map(([k, x]) => [k, fill(x)])) : v;
const at = (v, path) => path.split(".").reduce((x, k) => x == null ? undefined : x[k], v);
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

async function main() {
  const steps = JSON.parse(document.getElementById("steps").textContent);
  const boot = await (await fetch("/_pitwall/bootstrap")).json();
  const ws = new WebSocket(new URL(boot.url, location.href.replace(/^http/, "ws")));
  const pushed = [], waiting = new Map();
  let wake = () => {};
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.type === "result" && waiting.has(m.id)) waiting.get(m.id)(m);
    else { pushed.push(m); wake(); }
  };
  await new Promise((ok) => { ws.onopen = ok; });
  ws.send(JSON.stringify({ type: "hello", protocol: boot.protocol, token: boot.token, client: { name: "web-session", version: "1" } }));
  let n = 0;
  const call = (frame) => new Promise((ok) => { const id = String(++n); waiting.set(id, ok); ws.send(JSON.stringify({ ...frame, id })); });
  for (const [i, s] of steps.entries()) {
    if (s.wait) {
      const want = fill(s.wait);
      while (!pushed.some((m) => Object.entries(want).every(([k, v]) => same(m[k], v)))) await new Promise((ok) => { wake = ok; });
      log(`${i + 1} got ${want.type}`);
      continue;
    }
    for (let tries = 0; ; tries++) {
      const r = await call(fill(s.send));
      const good = Object.entries(fill(s.expect || {})).every(([p, v]) => same(at(r, p), v))
        && Object.values(s.save || {}).every((p) => typeof at(r, p) === "string" && at(r, p) !== "");
      if (good) {
        for (const [k, p] of Object.entries(s.save || {})) vars[k] = at(r, p);
        log(`${i + 1} ${s.send.type}: ok`);
        break;
      }
      if (!s.poll || tries > 300) { log(`${i + 1} ${s.send.type}: FAILED ${JSON.stringify(r)}`); return; }
      await new Promise((ok) => setTimeout(ok, 50));
    }
  }
  log("PASS");
}
main().catch((e) => log("FAILED " + e));
