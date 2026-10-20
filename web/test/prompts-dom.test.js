// Tests for the page wiring in static/prompts.js against a minimal fake DOM:
// what typing, clicking and picking do, and what is fetched. Run: node --test web/test
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');

const at = (mon, day, h, m) => new Date(2026, mon - 1, day, h, m).getTime();
const NOW = at(10, 7, 15, 40);

// The server's window JSON (see internal/web/prompts.go).
function raw(over) {
  return Object.assign({
    now: NOW, days: 3, widened: false, more: false, oldest: at(10, 7, 13, 0), matching: 5,
    agents: ['claude', 'codex'], repos: ['nosaka', 'sentient'], older: 0,
    menu: [{ id: 'A1234567', agent: 'claude', repo: 'nosaka' }, { id: 'B7654321', agent: 'codex', repo: 'sentient' }],
    sessions: [
      { id: 'B7654321', agent: 'codex', repo: 'sentient', started: at(10, 7, 15, 30), last: at(10, 7, 15, 40), prompts: 1, actions: 18, flags: 0 },
      { id: 'A1234567', agent: 'claude', repo: 'nosaka', started: at(10, 7, 13, 0), last: at(10, 7, 15, 26), prompts: 4, actions: 160, flags: 1 },
    ],
    prompts: [
      { id: 9, s: 0, t: at(10, 7, 15, 30), x: 'add a --json flag', d: 720, a: 18, f: 0, r: true },
      { id: 8, s: 1, t: at(10, 7, 15, 20), x: 'run the tests', d: 360, a: 14, f: 1 },
      { id: 7, s: 1, t: at(10, 7, 14, 45), x: 'make it use staging', d: 720, a: 37, f: 0 },
      { id: 6, s: 1, t: at(10, 7, 14, 2), x: 'fix the crash', b: 'log mentions snooze', d: 2160, a: 112, f: 0 },
      { id: 5, s: 1, t: at(10, 7, 13, 0), x: 'older prompt', d: 60, a: 2, f: 0 },
    ],
  }, over || {});
}

// setup builds the fake page, loads prompts.js against it (which wires it up)
// and returns handles to poke it.
function setup({ search = '', first = raw(), respond = () => raw() } = {}) {
  const els = {};
  const fetched = [];
  let docClick = null;
  const attrs = (e) => { e.attrs = {}; e.setAttribute = (k, v) => { e.attrs[k] = String(v); }; e.getAttribute = (k) => (k in e.attrs ? e.attrs[k] : null); };
  const mk = (id, extra) => {
    const e = Object.assign({ id, value: '', textContent: '', innerHTML: '', style: {}, hidden: false, handlers: {}, classList: { add() {}, remove() {} } }, extra);
    attrs(e);
    e.addEventListener = (ev, fn) => { e.handlers[ev] = fn; };
    els[id] = e;
    return e;
  };
  ['q', 'f-agent', 'f-repo', 'f-session', 'f-sort', 'f-flag', 'shown', 'view', 'empty', 'more', 'more-btn', 'more-text', 'total', 'wlabel', 'wcount'].forEach((id) => mk(id));
  mk('prompts-data', { textContent: JSON.stringify(first) });
  const chips = [3, 7, 30, 0].map((d) => { const c = { id: 'chip' + d }; attrs(c); c.setAttribute('data-days', d); return c; });
  mk('windowbar', { querySelectorAll: () => chips });

  const history = { last: null, replaceState(_s, _t, url) { history.last = url; } };
  const location = { search, pathname: '/prompts', href: '' };
  Object.assign(global, {
    document: { getElementById: (id) => els[id] || null, addEventListener: (ev, fn) => { if (ev === 'click') docClick = fn; } },
    location, history,
    fetch: (url) => { fetched.push(url); const r = respond(url); return Promise.resolve(r instanceof Error ? { ok: false, status: 500 } : { ok: true, json: () => Promise.resolve(r) }); },
  });
  delete require.cache[require.resolve('../static/prompts.js')];
  require('../static/prompts.js');

  // click simulates a document click on something matching the given selectors.
  const click = (matches) => {
    const target = { closest: (sel) => (matches[sel] ? Object.assign({ getAttribute: () => null }, matches[sel]) : null) };
    docClick({ target, preventDefault() {} });
  };
  const tick = () => new Promise((r) => setImmediate(r));
  return { els, fetched, history, location, chips, click, tick };
}

test.afterEach(() => { for (const k of ['document', 'location', 'history', 'fetch']) delete global[k]; });

test('the first render shows the embedded window as one list, newest first', () => {
  const p = setup();
  assert.doesNotMatch(p.els.view.innerHTML, /class="day"/);
  assert.ok(p.els.view.innerHTML.indexOf('add a --json flag') < p.els.view.innerHTML.indexOf('run the tests'));
  assert.equal(p.els.shown.textContent, '5 of 5 shown');
  assert.equal(p.els.total.textContent, 5);
  assert.equal(p.els.wlabel.textContent, 'the last 3 days');
  assert.match(p.els.wcount.textContent, /5 prompts/);
  assert.match(p.els['f-agent'].innerHTML, /<option value="">All agents<\/option><option value="claude">claude<\/option><option value="codex">codex<\/option>/);
  assert.match(p.els['f-session'].innerHTML, /<option value="A1234567">A123 · nosaka<\/option>/);
  assert.equal(p.els['f-sort'].value, 'date:desc');
  assert.equal(p.els.empty.style.display, 'none');
  assert.equal(p.els.more.hidden, true);
  assert.equal(p.chips.find((c) => c.getAttribute('data-days') === '3').getAttribute('aria-pressed'), 'true');
  assert.equal(p.history.last, '/prompts', 'the default state leaves the URL bare');
  assert.deepEqual(p.fetched, [], 'no request for the embedded window');
});

test('typing filters instantly, over the line and the body, and lands in the URL', () => {
  const p = setup();
  p.els.q.value = 'SNOOZE';
  p.els.q.handlers.input();
  assert.match(p.els.view.innerHTML, /fix the crash/);
  assert.doesNotMatch(p.els.view.innerHTML, /run the tests/);
  assert.equal(p.els.shown.textContent, '1 of 1 match');
  assert.equal(p.history.last, '/prompts?q=SNOOZE');
  assert.deepEqual(p.fetched, []);

  p.els.q.value = 'zzz';
  p.els.q.handlers.input();
  assert.equal(p.els.empty.style.display, 'block');
  assert.match(p.els.empty.innerHTML, /No match in the last 3 days\.<button class="chip" type="button" data-days="0" id="search-all">Search all time<\/button>/);
});

test('agent, repo and flagged filters', () => {
  const p = setup();
  p.els['f-agent'].value = 'codex';
  p.els['f-agent'].handlers.change();
  assert.match(p.els.view.innerHTML, /add a --json flag/);
  assert.doesNotMatch(p.els.view.innerHTML, /run the tests/);
  assert.equal(p.history.last, '/prompts?agent=codex');
  // the Session menu greys out sessions the filter excludes
  assert.match(p.els['f-session'].innerHTML, /<option value="A1234567" disabled>/);

  p.els['f-agent'].value = '';
  p.els['f-agent'].handlers.change();
  p.els['f-flag'].handlers.click();
  assert.match(p.els['f-flag'].getAttribute('aria-pressed'), /true/);
  assert.match(p.els.view.innerHTML, /run the tests/);
  assert.doesNotMatch(p.els.view.innerHTML, /make it use staging/);
  assert.equal(p.history.last, '/prompts?flagged=1');
});

test('clicking a column head sorts the list; again reverses; the Sort menu does the same', () => {
  const p = setup();
  p.click({ 'button.sort': { getAttribute: () => 'dur' } });
  assert.match(p.els.view.innerHTML, /aria-sort="descending"><button class="sort" type="button" data-sort="dur"/);
  assert.ok(p.els.view.innerHTML.indexOf('fix the crash') < p.els.view.innerHTML.indexOf('run the tests'));
  assert.equal(p.els['f-sort'].value, 'dur:desc');
  assert.equal(p.history.last, '/prompts?sort=dur%3Adesc');

  p.click({ 'button.sort': { getAttribute: () => 'dur' } });
  assert.match(p.els.view.innerHTML, /aria-sort="ascending"><button class="sort" type="button" data-sort="dur"/);
  assert.equal(p.history.last, '/prompts?sort=dur%3Aasc');

  // the phones' Sort menu does the same
  p.els['f-sort'].value = 'date:desc';
  p.els['f-sort'].handlers.change();
  assert.equal(p.history.last, '/prompts', 'newest first is the default and leaves the URL bare');
  p.els['f-sort'].value = 'acts:asc';
  p.els['f-sort'].handlers.change();
  assert.match(p.els.view.innerHTML, /aria-sort="ascending"><button class="sort" type="button" data-sort="acts"/);
});

test('picking a session fetches it, shows its summary, and going back clears it', async () => {
  const one = raw({ prompts: raw().prompts.filter((p) => p.s === 1).map((p) => ({ ...p, s: 0 })), sessions: [raw().sessions[1]], matching: 4 });
  const p = setup({ respond: () => one });
  p.els['f-session'].value = 'A1234567';
  p.els['f-session'].handlers.change();
  assert.deepEqual(p.fetched, ['/prompts/data?session=A1234567']);
  await p.tick();
  assert.match(p.els.view.innerHTML, /<div class="sumline">/);
  assert.match(p.els.view.innerHTML, /Open session A123 →/);
  assert.equal(p.els.shown.textContent, '4 of 4 shown');
  assert.equal(p.history.last, '/prompts?session=A1234567');
  assert.match(p.els['f-session'].innerHTML, /<option value="A1234567" selected>/);

  p.els['f-session'].value = '';
  p.els['f-session'].handlers.change();
  assert.doesNotMatch(p.els.view.innerHTML, /sumline/);
  assert.equal(p.els.shown.textContent, '5 of 5 shown');
  assert.equal(p.history.last, '/prompts');
});

test('a time-window chip fetches that window and shows it', async () => {
  const wider = raw({ days: 7, widened: true });
  const p = setup({ respond: () => wider });
  p.click({ '[data-days]': { getAttribute: () => '7' } });
  assert.deepEqual(p.fetched, ['/prompts/data?days=7']);
  await p.tick();
  assert.equal(p.els.wlabel.textContent, 'the last 7 days (widened to the newest 5)');
  assert.equal(p.chips.find((c) => c.getAttribute('data-days') === '7').getAttribute('aria-pressed'), 'true');
  assert.equal(p.history.last, '/prompts?days=7');
});

test('a truncated window offers "Load older", which fetches before the oldest prompt and merges', async () => {
  const older = raw({ more: false, prompts: [{ id: 1, s: 0, t: at(10, 6, 9, 0), x: 'ancient', d: 1, a: 1, f: 0 }], sessions: [raw().sessions[1]] });
  const p = setup({ first: raw({ more: true }), respond: () => older });
  // all five loaded prompts are shown, so the button loads older ones from the server
  assert.equal(p.els.more.hidden, false);
  assert.equal(p.els['more-btn'].textContent, 'Load older');
  p.els['more-btn'].onclick();
  assert.equal(p.fetched.length, 1);
  assert.match(p.fetched[0], new RegExp('^/prompts/data\\?days=3&before=' + encodeURIComponent(new Date(at(10, 7, 13, 0)).toISOString()).replace(/\./g, '\\.') + '$'));
  await p.tick();
  assert.equal(p.els.more.hidden, true, 'nothing more to load');
  assert.equal(p.els.total.textContent, 6);
  assert.match(p.els.view.innerHTML, /ancient/);
});

test('"Show more" reveals more prompts without a request', () => {
  const sessions = [{ id: 'S0000000', agent: 'claude', repo: 'r', started: at(10, 7, 9, 0), last: at(10, 7, 10, 0), prompts: 120, actions: 120, flags: 0 }];
  const prompts = [];
  for (let i = 0; i < 120; i++) prompts.push({ id: 100 + i, s: 0, t: at(10, 7, 9, 0) + (120 - i) * 1000, x: 'p' + i, d: 60, a: 1, f: 0 });
  const p = setup({ first: raw({ sessions, prompts, menu: [], matching: 120 }) });
  assert.equal(p.els.shown.textContent, '100 of 120 shown');
  assert.equal(p.els['more-btn'].textContent, 'Show more');
  assert.equal(p.els['more-text'].textContent, '20 older prompts');
  p.els['more-btn'].onclick();
  assert.equal(p.els.shown.textContent, '120 of 120 shown');
  assert.equal(p.els.more.hidden, true);
  assert.deepEqual(p.fetched, []);
});

test('the URL restores filters, sort and session on load', async () => {
  const one = raw({ prompts: raw().prompts.filter((p) => p.s === 1).map((p) => ({ ...p, s: 0 })), sessions: [raw().sessions[1]] });
  const p = setup({ search: '?q=Fix&agent=claude&flagged=1&sort=acts%3Adesc&session=A1234567', respond: () => one });
  assert.equal(p.els.q.value, 'Fix');
  assert.deepEqual(p.fetched, ['/prompts/data?session=A1234567']);
  await p.tick();
  assert.equal(p.els['f-sort'].value, 'acts:desc');
  assert.equal(p.els['f-flag'].getAttribute('aria-pressed'), 'true');
  assert.equal(p.history.last, '/prompts?q=Fix&agent=claude&session=A1234567&flagged=1&sort=acts%3Adesc');
  // days in the URL that differ from the embedded window are fetched too
  const q = setup({ search: '?days=30', respond: () => raw({ days: 30 }) });
  assert.deepEqual(q.fetched, ['/prompts/data?days=30']);
});

test('a failed load says so instead of showing nothing', async () => {
  const p = setup({ respond: () => new Error('boom') });
  p.click({ '[data-days]': { getAttribute: () => '30' } });
  await p.tick();
  assert.equal(p.els.empty.style.display, 'block');
  assert.match(p.els.empty.textContent, /Could not load prompts \(HTTP 500\)\. Is sentientd running\?/);
});
