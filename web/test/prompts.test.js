// Tests for static/prompts.js. Run: node --test web/test   (make jstest)
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const P = require('../static/prompts.js');

// Local-time helpers: the script formats in the browser's time zone, so the
// tests build their dates the same way instead of assuming one.
const at = (mon, day, h, m) => new Date(2026, mon - 1, day, h, m).getTime();
const NOW = at(10, 7, 15, 40);

const session = (id, agent, repo, over) => Object.assign({ id, agent, repo, started: at(10, 7, 9, 0), last: at(10, 7, 14, 0), prompts: 10, actions: 50, flags: 0 }, over || {});
const prompt = (id, sid, t, over) => Object.assign({ id, sid, t, x: 'prompt ' + id, b: '', p: 0, d: 300, a: 4, f: 0, r: false }, over || {});

function windowData() {
  const sessions = { A1234567: session('A1234567', 'claude', 'nosaka', { prompts: 128 }), B7654321: session('B7654321', 'codex', 'sentient', { prompts: 1 }) };
  const prompts = [
    prompt(9, 'B7654321', at(10, 7, 15, 30), { r: true, d: 720, a: 18, x: 'add a --json flag' }),
    prompt(8, 'A1234567', at(10, 7, 15, 20), { f: 1, d: 360, a: 14, x: 'run the tests' }),
    prompt(7, 'A1234567', at(10, 7, 14, 45), { d: 720, a: 37, x: 'make it use staging' }),
    prompt(6, 'A1234567', at(10, 7, 14, 2), { d: 2160, a: 112, x: 'fix the crash', b: 'pasted log mentions snooze' }),
    prompt(5, 'A1234567', at(10, 7, 13, 0), { d: 60, a: 2, x: 'older prompt' }),
  ];
  return { prompts, sessions, order: ['B7654321', 'A1234567'], now: NOW, days: 3, widened: false, more: false, oldest: prompts[4].t, matching: 5, agents: ['claude', 'codex'], repos: ['nosaka', 'sentient'], menu: [{ id: 'A1234567', agent: 'claude', repo: 'nosaka' }, { id: 'B7654321', agent: 'codex', repo: 'sentient' }, { id: 'C0000000', agent: 'claude', repo: '' }], older: 3 };
}
const baseState = (over) => Object.assign({
  win: windowData(), one: null, session: '', f: { q: '', agent: '', repo: '', flagged: false },
  sort: { key: 'date', asc: false }, days: 3, rows: P.ROW_STEP,
}, over || {});

test('esc escapes everything that matters in HTML', () => {
  assert.equal(P.esc(`<a href="x" onclick='y'>&</a>`), '&lt;a href=&quot;x&quot; onclick=&#39;y&#39;&gt;&amp;&lt;/a&gt;');
  assert.equal(P.esc(5), '5');
});

test('fmtDur', () => {
  assert.equal(P.fmtDur(0, false), '<1m');
  assert.equal(P.fmtDur(59, false), '<1m');
  assert.equal(P.fmtDur(720, false), '12m');
  assert.equal(P.fmtDur(3600, false), '1h');
  assert.equal(P.fmtDur(3900, false), '1h 5m');
  assert.equal(P.fmtDur(720, true), '12m+');
});

test('fmtDate drops the year only for the current one', () => {
  assert.equal(P.fmtDate(at(10, 7, 9, 5), NOW), 'Oct 7 09:05');
  assert.equal(P.fmtDate(new Date(2025, 11, 30, 23, 59).getTime(), NOW), 'Dec 30 2025 23:59');
});

test('timeRange', () => {
  assert.equal(P.timeRange(0, 0, NOW), '');
  assert.equal(P.timeRange(at(10, 7, 14, 0), at(10, 7, 14, 0) + 30000, NOW), 'Oct 7 14:00');
  assert.equal(P.timeRange(at(10, 7, 14, 0), at(10, 7, 15, 30), NOW), 'Oct 7 14:00–15:30');
  assert.equal(P.timeRange(at(10, 6, 22, 0), at(10, 7, 1, 0), NOW), 'Oct 6 22:00 – Oct 7 01:00');
});

test('normalize gives each prompt its session id and merge adds older pages without duplicates', () => {
  const raw = {
    now: NOW, days: 3, sessions: [{ id: 'S1', agent: 'claude', repo: 'r' }, { id: 'S2', agent: 'codex', repo: 'q' }],
    prompts: [{ id: 2, s: 1, t: 2, x: 'b', d: 1, a: 1, f: 0 }, { id: 1, s: 0, t: 1, x: 'a', b: 'body', p: 3, d: 2, a: 2, f: 1, r: true }],
    more: true, oldest: 1, matching: 5, agents: ['claude'], repos: ['r'], menu: [], older: 0,
  };
  const d = P.normalize(raw);
  assert.deepEqual(d.prompts.map(p => [p.id, p.sid]), [[2, 'S2'], [1, 'S1']]);
  assert.equal(d.prompts[1].b, 'body');
  assert.equal(d.prompts[1].r, true);
  assert.deepEqual(d.order, ['S1', 'S2']);

  const older = P.normalize({
    now: NOW, days: 3, sessions: [{ id: 'S1', agent: 'claude', repo: 'r', prompts: 9 }, { id: 'S3', agent: 'claude', repo: 'z' }],
    prompts: [{ id: 1, s: 0, t: 1, x: 'a', d: 2, a: 2, f: 1 }, { id: 0, s: 1, t: 0, x: 'old', d: 1, a: 1, f: 0 }],
    more: false, oldest: 0, matching: 5, agents: [], repos: [], menu: [], older: 0,
  });
  P.merge(d, older);
  assert.deepEqual(d.prompts.map(p => p.id), [2, 1, 0]); // 1 was already there
  assert.deepEqual(d.order, ['S1', 'S2', 'S3']);
  assert.equal(d.sessions.S1.prompts, 9);
  assert.equal(d.more, false);
  assert.equal(d.oldest, 0);
});

test('matches: agent, repo, flagged, and text over the line and the body', () => {
  const s = session('A', 'claude', 'nosaka');
  const p = prompt(1, 'A', 1, { x: 'Fix the Crash', b: 'log mentions Snooze', f: 1 });
  const none = { q: '', agent: '', repo: '', flagged: false };
  assert.ok(P.matches(p, s, none));
  assert.ok(P.matches(p, s, { ...none, agent: 'claude', repo: 'nosaka', flagged: true, q: 'crash' }));
  assert.ok(P.matches(p, s, { ...none, q: 'snooze' }), 'the body is searched too');
  assert.ok(!P.matches(p, s, { ...none, agent: 'codex' }));
  assert.ok(!P.matches(p, s, { ...none, repo: 'rly' }));
  assert.ok(!P.matches({ ...p, f: 0 }, s, { ...none, flagged: true }));
  assert.ok(!P.matches(p, s, { ...none, q: 'nothing' }));
  assert.ok(P.matches(p, undefined, none), 'a prompt whose session is unknown still lists');
});

test('sortRows: columns, direction, and ties go to the newest', () => {
  const rows = windowData().prompts;
  const ids = (r) => r.map(p => p.id);
  assert.deepEqual(ids(P.sortRows(rows, 'date', false)), [9, 8, 7, 6, 5]);
  assert.deepEqual(ids(P.sortRows(rows, 'date', true)), [5, 6, 7, 8, 9]);
  assert.deepEqual(ids(P.sortRows(rows, 'dur', false)), [6, 9, 7, 8, 5]); // 7 and 9 tie at 12m: the newer (9) first
  assert.deepEqual(ids(P.sortRows(rows, 'acts', false)), [6, 7, 9, 8, 5]);
  assert.deepEqual(ids(P.sortRows(rows, 'acts', true)), [5, 8, 9, 7, 6]);
  assert.deepEqual(ids(P.sortRows(rows, 'flags', false)), [8, 9, 7, 6, 5]);
  // 'add a --json flag' < 'fix the crash' < 'make it use staging' < 'older prompt' < 'run the tests'
  assert.deepEqual(ids(P.sortRows(rows, 'prompt', true)), [9, 6, 7, 5, 8]);
  assert.deepEqual(ids(rows), [9, 8, 7, 6, 5], 'sorting does not reorder the input');
});

test('rowHTML: link, badge, escaping, running, flags; the flat form adds the session', () => {
  const s = session('A1234567', 'claude', 'nosaka');
  const p = prompt(7, 'A1234567', at(10, 7, 14, 45), { x: '<b>hi</b>', p: 240, r: true, f: 2, d: 720, a: 37 });
  const h = P.rowHTML(p, s, false, NOW);
  assert.match(h, /<tr data-href="\/prompt\/7">/);
  assert.match(h, /<span class="pasted">pasted 240 lines<\/span> &lt;b&gt;hi&lt;\/b&gt;/);
  assert.match(h, /<span class="running-dot"/);
  assert.match(h, /<time datetime="2026-10-07T14:45">Oct 7 14:45<\/time>/);
  assert.match(h, /<td class="dur">12m\+<\/td>/);
  assert.match(h, /title="37 actions so far">37<\/span>/);
  assert.match(h, /<span class="flag" title="2 attention items">2<\/span>/);
  assert.match(h, /<span class="flag">2 flags<\/span><span class="running-label">running<\/span>/);
  assert.doesNotMatch(h, /sesscell/);

  const f = P.rowHTML(p, s, true, NOW);
  assert.match(f, /<td class="sesscell"><a class="sid" href="\/session\/A1234567" title="Open session A123">A123<\/a>.*<span class="repo">nosaka<\/span><\/td>/);
  assert.match(f, /<span class="m-only"> · A123 · nosaka · 12m\+/);
});

test('build, the default: one list, newest first, each row names its session', () => {
  const v = P.build(baseState());
  assert.equal(v.mode, 'flat');
  assert.equal(v.total, 5);
  assert.match(v.shown, /^5 of 5 shown$/);
  assert.equal((v.html.match(/<tr data-href/g) || []).length, 5);
  assert.doesNotMatch(v.html, /class="day"|sortnote/);
  assert.match(v.html, /<th>Session<\/th>/);
  assert.match(v.html, /<a class="sid" href="\/session\/B7654321"/);
  assert.ok(v.html.indexOf('add a --json flag') < v.html.indexOf('run the tests'), 'newest first, sessions interleaved');
  assert.equal(v.more, null);
  assert.equal(v.empty, '');

  const truncated = P.build(baseState({ win: { ...windowData(), more: true } }));
  assert.deepEqual(truncated.more, { older: true, text: 'older prompts not loaded yet' });
});

test('build, filtering: text, flagged and counts', () => {
  const v = P.build(baseState({ f: { q: 'snooze', agent: '', repo: '', flagged: false } }));
  assert.equal(v.total, 1);
  assert.match(v.shown, /^1 of 1 match$/);

  const fl = P.build(baseState({ f: { q: '', agent: '', repo: '', flagged: true } }));
  assert.equal(fl.total, 1);
  assert.match(fl.html, /run the tests/);

  const codex = P.build(baseState({ f: { q: '', agent: 'codex', repo: '', flagged: false } }));
  assert.equal(codex.total, 1);
  assert.doesNotMatch(codex.html, /A123/);
});

test('build, a column sort reorders the one list and marks the column', () => {
  const v = P.build(baseState({ sort: { key: 'dur', asc: false } }));
  assert.equal(v.mode, 'flat');
  assert.match(v.html, /<th class="sortable" aria-sort="descending"><button class="sort" type="button" data-sort="dur">Duration<\/button><\/th>/);
  assert.ok(v.html.indexOf('fix the crash') < v.html.indexOf('make it use staging'), 'longest first');
  assert.match(v.shown, /^5 of 5 shown$/);

  const cut = P.build(baseState({ sort: { key: 'dur', asc: false }, rows: 2 }));
  assert.match(cut.shown, /^2 of 5 shown$/);
  assert.deepEqual(cut.more, { reveal: true, text: '3 older prompts' });
  assert.equal((cut.html.match(/<tr data-href/g) || []).length, 2);
});

test('build, one session: summary line, its rows, sorted', () => {
  const one = windowData();
  one.prompts = one.prompts.filter(p => p.sid === 'A1234567');
  const v = P.build(baseState({ session: 'A1234567', one, sort: { key: 'acts', asc: false } }));
  assert.equal(v.mode, 'one');
  assert.match(v.html, /<div class="sumline"><span class="repo">nosaka<\/span>/);
  assert.match(v.html, /128 prompts<\/span><span>50 actions/);
  assert.match(v.html, /<a class="open" href="\/session\/A1234567">Open session A123 →<\/a>/);
  assert.ok(v.html.indexOf('fix the crash') < v.html.indexOf('run the tests'), 'most actions first');
  assert.doesNotMatch(v.html, /class="day"/);
  assert.match(v.shown, /^4 of 4 shown$/);

  const none = P.build(baseState({ session: 'A1234567', one, f: { q: 'zzz', agent: '', repo: '', flagged: false } }));
  assert.equal(none.empty, 'No prompts in this session match these filters.');
  assert.match(none.shown, /0 of 0 match/);
});

test('build, empty windows say what to do', () => {
  const empty = { ...windowData(), prompts: [], sessions: {}, order: [] };
  const quiet = P.build(baseState({ win: empty }));
  assert.equal(quiet.empty, 'Nothing in the last 3 days.');
  assert.equal(quiet.searchAll, true);
  const nomatch = P.build(baseState({ f: { q: 'zzz', agent: '', repo: '', flagged: false } }));
  assert.equal(nomatch.empty, 'No match in the last 3 days.');
  assert.equal(nomatch.searchAll, true);
  const all = P.build(baseState({ win: empty, days: 0 }));
  assert.match(all.empty, /^No prompts yet/);
  assert.equal(all.searchAll, false);
  assert.equal(P.build(baseState({ days: 0, f: { q: 'zzz', agent: '', repo: '', flagged: false } })).empty, 'No prompts match.');
});

test('effectiveDays: a widened window lights the chip that really covers the data', () => {
  const day = 864e5;
  const win = (widened, ...ageDays) => ({ widened, now: NOW, prompts: ageDays.map((a, i) => ({ id: i, t: NOW - a * day })) });
  // not widened: what was asked for
  assert.equal(P.effectiveDays(win(false, 1, 2), 3), 3);
  // widened, but still inside 3 days (fewer than the minimum prompts)
  assert.equal(P.effectiveDays(win(true, 0.5, 2.9), 3), 3);
  // widened past 3 days: the smallest chip that reaches the oldest prompt
  assert.equal(P.effectiveDays(win(true, 1, 5), 3), 7);
  assert.equal(P.effectiveDays(win(true, 1, 25), 3), 30);
  assert.equal(P.effectiveDays(win(true, 1, 103), 3), 0, 'older than 30 days: all time');
  // a chip asked for is never narrowed: a widened 7-day window skips the 3-day chip
  assert.equal(P.effectiveDays(win(true, 1, 6.5), 7), 7);
  assert.equal(P.effectiveDays(win(true, 1, 25), 7), 30);
  // all time stays all time; an empty widened window falls back to the asked-for chip
  assert.equal(P.effectiveDays(win(true, 1, 200), 0), 0);
  assert.equal(P.effectiveDays(win(true), 3), 3);
});

test('sort values round-trip and column heads reverse or switch', () => {
  assert.deepEqual(P.parseSort('dur:asc'), { key: 'dur', asc: true });
  assert.deepEqual(P.parseSort('nope'), { key: 'date', asc: false });
  assert.deepEqual(P.parseSort(''), { key: 'date', asc: false });
  assert.deepEqual(P.parseSort('prompt:desc'), { key: 'prompt', asc: false });
  assert.equal(P.sortParam({ key: 'acts', asc: false }), 'acts:desc');
  const cur = { key: 'date', asc: false };
  assert.deepEqual(P.nextSort(cur, 'date'), { key: 'date', asc: true });          // same head: reverse
  assert.deepEqual(P.nextSort(cur, 'dur'), { key: 'dur', asc: false });          // numbers high first
  assert.deepEqual(P.nextSort(cur, 'prompt'), { key: 'prompt', asc: true });     // text A to Z
  assert.ok(P.isDefaultSort('date', false) && !P.isDefaultSort('date', true) && !P.isDefaultSort('dur', false));
  assert.equal(P.sortName({ key: 'prompt', asc: false }), 'prompt text, Z to A');
  assert.equal(P.sortName({ key: 'flags', asc: true }), 'flags, fewest first');
  assert.equal(P.sortName({ key: 'acts', asc: false }), 'actions, most first');
  assert.equal(P.sortName({ key: 'date', asc: true }), 'date, oldest first');
});

test('sessionOptions: recent sessions, filtered ones disabled, the selected one kept, older noted', () => {
  const d = windowData();
  const none = { agent: '', repo: '' };
  let o = P.sessionOptions(d, '', none);
  assert.deepEqual(o.map(x => x.label), ['A123 · nosaka', 'B765 · sentient', 'C000 · claude', '3 older sessions…']);
  assert.equal(o[3].disabled, true);
  o = P.sessionOptions(d, 'A1234567', { agent: 'codex', repo: '' });
  assert.deepEqual(o.map(x => x.disabled), [true, false, true, true]);
  assert.equal(o[0].selected, true);
  // a selected session that isn't in the menu is still offered
  const lone = windowData();
  lone.menu = []; lone.older = 0;
  o = P.sessionOptions(lone, 'B7654321', none);
  assert.deepEqual(o.map(x => [x.value, x.selected]), [['B7654321', true]]);
});

test('windowLabel', () => {
  assert.equal(P.windowLabel(3), 'the last 3 days');
  assert.equal(P.windowLabel(1), 'the last 1 day');
  assert.equal(P.windowLabel(0), 'all time');
});
