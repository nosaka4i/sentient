// The prompts page. The server hands over a window of prompts as JSON
// (see internal/web/prompts.go); everything here — search, filters, sorting,
// paging — happens in the browser on that data. The logic is plain
// functions (tested with node: `make jstest`); the DOM wiring is at the bottom.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.SentientPrompts = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  var DEFAULT_DAYS = 3;
  var ROW_STEP = 100;    // prompts "Show more" adds in a list

  // ---- formatting ----

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function two(n) { return n < 10 ? '0' + n : String(n); }
  function plural(n, word) { return n + ' ' + word + (n === 1 ? '' : 's'); }
  function shortId(id) { return String(id).slice(0, 4); }

  function fmtClock(ms) { var d = new Date(ms); return two(d.getHours()) + ':' + two(d.getMinutes()); }
  // "Oct 7 15:30", with the year when it isn't the current one.
  function fmtDate(ms, nowMs) {
    var d = new Date(ms), n = new Date(nowMs);
    var s = MONTHS[d.getMonth()] + ' ' + d.getDate();
    if (d.getFullYear() !== n.getFullYear()) s += ' ' + d.getFullYear();
    return s + ' ' + fmtClock(ms);
  }
  function isoMinute(ms) {
    var d = new Date(ms);
    return d.getFullYear() + '-' + two(d.getMonth() + 1) + '-' + two(d.getDate()) + 'T' + fmtClock(ms);
  }
  function sameDay(a, b) {
    var x = new Date(a), y = new Date(b);
    return x.getFullYear() === y.getFullYear() && x.getMonth() === y.getMonth() && x.getDate() === y.getDate();
  }
  // "Oct 7 14:02–15:26", repeating the date when the range spans days.
  function timeRange(from, to, nowMs) {
    if (!from) return '';
    var s = fmtDate(from, nowMs);
    if (!to || to - from < 60000) return s;
    return sameDay(from, to) ? s + '–' + fmtClock(to) : s + ' – ' + fmtDate(to, nowMs);
  }
  // A prompt's duration: "<1m", "12m", "1h 5m"; a "+" while it is still running.
  function fmtDur(sec, running) {
    var m = Math.floor(sec / 60), s;
    if (m < 1) s = '<1m';
    else if (m < 60) s = m + 'm';
    else s = (m % 60 === 0) ? (m / 60) + 'h' : Math.floor(m / 60) + 'h ' + (m % 60) + 'm';
    return running ? s + '+' : s;
  }

  // ---- data ----

  // normalize turns the server's window into prompts that carry their session id.
  function normalize(raw) {
    var sessions = {}, order = [];
    (raw.sessions || []).forEach(function (s) { sessions[s.id] = s; order.push(s.id); });
    var prompts = (raw.prompts || []).map(function (p) {
      var s = raw.sessions[p.s];
      return { id: p.id, sid: s ? s.id : '', t: p.t, x: p.x, b: p.b || '', p: p.p || 0, d: p.d, a: p.a, f: p.f, r: !!p.r };
    });
    return {
      prompts: prompts, sessions: sessions, order: order, now: raw.now, days: raw.days,
      widened: !!raw.widened, more: !!raw.more, oldest: raw.oldest, matching: raw.matching,
      agents: raw.agents || [], repos: raw.repos || [], menu: raw.menu || [], older: raw.older || 0
    };
  }
  // merge adds an older page to a dataset.
  function merge(into, page) {
    var have = {};
    into.prompts.forEach(function (p) { have[p.id] = true; });
    page.prompts.forEach(function (p) { if (!have[p.id]) into.prompts.push(p); });
    page.order.forEach(function (id) { if (!into.sessions[id]) into.order.push(id); into.sessions[id] = page.sessions[id]; });
    into.more = page.more; into.oldest = page.oldest;
    return into;
  }

  function matches(p, s, f) {
    s = s || {};
    if (f.agent && s.agent !== f.agent) return false;
    if (f.repo && s.repo !== f.repo) return false;
    if (f.flagged && !p.f) return false;
    if (f.q && (p.x + ' ' + p.b).toLowerCase().indexOf(f.q) < 0) return false;
    return true;
  }

  var SORT_VALUE = {
    prompt: function (p) { return p.x.toLowerCase(); },
    date: function (p) { return p.t; },
    dur: function (p) { return p.d; },
    acts: function (p) { return p.a; },
    flags: function (p) { return p.f; }
  };
  // sortRows orders rows by a column; ties go to the newest prompt. asc flips
  // the column's order (newest/highest first is the default).
  function sortRows(rows, key, asc) {
    var val = SORT_VALUE[key] || SORT_VALUE.date;
    return rows.slice().sort(function (a, b) {
      var x = val(a), y = val(b), c = x < y ? -1 : x > y ? 1 : 0;
      if (!asc) c = -c;
      return c || (b.t - a.t) || (b.id - a.id);
    });
  }
  function isDefaultSort(key, asc) { return key === 'date' && !asc; }
  function firstDirAsc(key) { return key === 'prompt'; }

  // ---- rendering (strings) ----

  function agentIcon(agent) {
    if (agent !== 'claude' && agent !== 'codex') return esc(agent || '');
    return '<svg class="agent-ic" role="img" aria-label="' + agent + '"><title>' + agent + '</title><use href="#i-' + agent + '"/></svg>';
  }

  var HEADS = [['prompt', 'Prompt'], ['date', 'Date'], ['dur', 'Duration'], ['acts', 'Actions'], ['flags', 'Flags']];
  function headHTML(sort, flat) {
    var h = '<thead><tr><th><span class="visually-hidden" style="position:absolute;left:-9999px">State</span></th>';
    HEADS.forEach(function (c) {
      if (flat && c[0] === 'date') h += '<th>Session</th>';
      var aria = c[0] === sort.key ? (sort.asc ? 'ascending' : 'descending') : 'none';
      h += '<th class="sortable" aria-sort="' + aria + '"><button class="sort" type="button" data-sort="' + c[0] + '">' + c[1] + '</button></th>';
    });
    return h + '</tr></thead>';
  }

  // rowHTML is one prompt's table row; flat adds the Session column.
  function rowHTML(p, s, flat, nowMs) {
    s = s || {};
    var href = '/prompt/' + p.id, sess = '/session/' + encodeURIComponent(p.sid);
    var meta = ' · ' + (flat ? shortId(p.sid) + (s.repo ? ' · ' + esc(s.repo) : '') + ' · ' : '') +
      fmtDur(p.d, p.r) + ' · ' + p.a + ' actions' +
      (p.f ? ' · <span class="flag">' + plural(p.f, 'flag') + '</span>' : '') +
      (p.r ? '<span class="running-label">running</span>' : '');
    var h = '<tr data-href="' + href + '">' +
      '<td class="state">' + (p.r ? '<span class="running-dot" role="img" aria-label="running" title="Still running"></span>' : '') + '</td>' +
      '<td class="prompt"><a href="' + href + '" title="' + esc(p.x) + '">' +
      (p.p ? '<span class="pasted">pasted ' + p.p + ' lines</span> ' : '') + esc(p.x) + '</a></td>';
    if (flat) {
      h += '<td class="sesscell"><a class="sid" href="' + sess + '" title="Open session ' + esc(shortId(p.sid)) + '">' + esc(shortId(p.sid)) + '</a>' +
        agentIcon(s.agent) + (s.repo ? '<span class="repo">' + esc(s.repo) + '</span>' : '') + '</td>';
    }
    h += '<td class="date metaline"><time datetime="' + isoMinute(p.t) + '">' + fmtDate(p.t, nowMs) + '</time><span class="m-only">' + meta + '</span></td>' +
      '<td class="dur">' + fmtDur(p.d, p.r) + '</td>' +
      '<td class="acts"><span class="acts" title="' + p.a + ' actions' + (p.r ? ' so far' : '') + '">' + p.a + '</span></td>' +
      '<td class="flags">' + (p.f ? '<span class="flag" title="' + plural(p.f, 'attention item') + '">' + p.f + '</span>' : '') + '</td></tr>';
    return h;
  }

  // sumHTML is the one-line description of the selected session.
  function sumHTML(s, nowMs) {
    var range = timeRange(s.started, s.last, nowMs);
    var h = '<div class="sumline">' + (s.repo ? '<span class="repo">' + esc(s.repo) + '</span>' : '') +
      (range ? '<span>' + range + '</span>' : '') +
      '<span>' + plural(s.prompts, 'prompt') + '</span><span>' + s.actions + ' actions</span>' +
      (s.flags ? '<span><span class="flag">' + plural(s.flags, 'flag') + '</span></span>' : '') +
      '<a class="open" href="/session/' + encodeURIComponent(s.id) + '">Open session ' + esc(shortId(s.id)) + ' →</a></div>';
    return h;
  }

  function windowLabel(days) { return days === 0 ? 'all time' : 'the last ' + plural(days, 'day'); }

  // ---- the view ----

  // build computes what the page shows for a state: the HTML of the list, the
  // text beside the filters, whether and how to "show more", and an empty message.
  //   state: { win, one, session, f:{q,agent,repo,flagged}, sort:{key,asc}, days,
  //            rows }   (rows: how many prompts are revealed)
  function build(state) {
    var nowMs = state.win.now;
    var f = state.f, filtering = !!(f.q || f.agent || f.repo || f.flagged);
    var data = state.session && state.one ? state.one : state.win;
    var out = { html: '', shown: '', more: null, empty: '', mode: '', total: 0, loaded: data.prompts.length };

    var rows = data.prompts.filter(function (p) { return matches(p, data.sessions[p.sid], f); });
    out.total = rows.length;

    if (state.session) {
      out.mode = 'one';
      var s = data.sessions[state.session] || state.win.sessions[state.session];
      rows = sortRows(rows, state.sort.key, state.sort.asc);
      var shown = rows.slice(0, state.rows);
      out.html = (s ? sumHTML(s, nowMs) : '');
      if (shown.length) {
        out.html += '<div class="table-scroll"><table class="prompts">' + headHTML(state.sort, false) + '<tbody>' +
          shown.map(function (p) { return rowHTML(p, s, false, nowMs); }).join('') + '</tbody></table></div>';
      } else {
        out.empty = 'No prompts in this session match these filters.';
      }
      out.shown = shown.length + ' of ' + rows.length + (filtering ? ' match' : ' shown');
      if (shown.length < rows.length) out.more = { reveal: true, text: (rows.length - shown.length) + ' older prompts in this session' };
      else if (data.more) out.more = { older: true, text: 'older prompts not loaded yet' };
      return out;
    }

    // all sessions: one list, newest first unless a column is sorted; the Session column links to each session
    out.mode = 'flat';
    rows = sortRows(rows, state.sort.key, state.sort.asc);
    var shownRows = rows.slice(0, state.rows);
    if (shownRows.length) {
      out.html = '<div class="table-scroll"><table class="prompts">' + headHTML(state.sort, true) + '<tbody>' +
        shownRows.map(function (p) { return rowHTML(p, data.sessions[p.sid], true, nowMs); }).join('') + '</tbody></table></div>';
    }
    out.shown = shownRows.length + ' of ' + rows.length + (filtering ? ' match' : ' shown');
    if (shownRows.length < rows.length) out.more = { reveal: true, text: (rows.length - shownRows.length) + ' older prompts' };
    else if (data.more) out.more = { older: true, text: 'older prompts not loaded yet' };

    if (!out.html) {
      if (state.days === 0) out.empty = filtering ? 'No prompts match.' : 'No prompts yet. They show up here as soon as an agent starts working.';
      else out.empty = (filtering ? 'No match in ' : 'Nothing in ') + windowLabel(state.days) + '.';
      out.searchAll = state.days !== 0;
    }
    return out;
  }

  function sortName(sort) {
    switch (sort.key) {
      case 'prompt': return 'prompt text, ' + (sort.asc ? 'A to Z' : 'Z to A');
      case 'date': return 'date, ' + (sort.asc ? 'oldest first' : 'newest first');
      case 'dur': return 'duration, ' + (sort.asc ? 'shortest first' : 'longest first');
      case 'acts': return 'actions, ' + (sort.asc ? 'fewest first' : 'most first');
      default: return 'flags, ' + (sort.asc ? 'fewest first' : 'most first');
    }
  }

  // parseSort reads "dur:asc" (the URL and Sort menu form); anything else is newest first.
  function parseSort(s) {
    var m = /^(prompt|date|dur|acts|flags):(asc|desc)$/.exec(s || '');
    return m ? { key: m[1], asc: m[2] === 'asc' } : { key: 'date', asc: false };
  }
  function sortParam(sort) { return sort.key + ':' + (sort.asc ? 'asc' : 'desc'); }
  // nextSort is what clicking a column head does: reverse it if it is the
  // sorted column, otherwise sort by it (text A to Z, numbers high first).
  function nextSort(cur, key) {
    return key === cur.key ? { key: key, asc: !cur.asc } : { key: key, asc: firstDirAsc(key) };
  }

  // sessionOptions builds the Session menu: recent sessions (those the agent
  // and repo filters exclude are disabled), plus the selected one if it is older.
  function sessionOptions(data, selected, f) {
    var out = [], found = false;
    data.menu.forEach(function (m) {
      var ok = (!f.agent || m.agent === f.agent) && (!f.repo || m.repo === f.repo);
      if (m.id === selected) found = true;
      out.push({ value: m.id, label: shortId(m.id) + ' · ' + (m.repo || m.agent), disabled: !ok, selected: m.id === selected });
    });
    var s = selected && data.sessions[selected];
    if (selected && !found && s) out.push({ value: s.id, label: shortId(s.id) + ' · ' + (s.repo || s.agent), disabled: false, selected: true });
    if (data.older > 0) out.push({ value: '', label: plural(data.older, 'older session') + '…', disabled: true, selected: false });
    return out;
  }

  var api = {
    esc: esc, fmtDate: fmtDate, fmtClock: fmtClock, fmtDur: fmtDur, timeRange: timeRange, shortId: shortId,
    normalize: normalize, merge: merge, matches: matches, sortRows: sortRows,
    rowHTML: rowHTML, headHTML: headHTML, sumHTML: sumHTML, build: build,
    parseSort: parseSort, sortParam: sortParam, nextSort: nextSort, sortName: sortName, sessionOptions: sessionOptions,
    windowLabel: windowLabel, isDefaultSort: isDefaultSort,
    DEFAULT_DAYS: DEFAULT_DAYS, ROW_STEP: ROW_STEP
  };

  // ---- the page ----

  if (typeof document !== 'undefined' && document.getElementById('prompts-data')) init();

  function init() {
    var $ = function (id) { return document.getElementById(id); };
    var el = {
      q: $('q'), agent: $('f-agent'), repo: $('f-repo'), session: $('f-session'), sort: $('f-sort'), flag: $('f-flag'),
      shown: $('shown'), view: $('view'), empty: $('empty'), more: $('more'), moreBtn: $('more-btn'), moreText: $('more-text'),
      total: $('total'), wlabel: $('wlabel'), wcount: $('wcount'), wbar: $('windowbar')
    };
    var state = {
      win: normalize(JSON.parse($('prompts-data').textContent)), one: null, session: '',
      f: { q: '', agent: '', repo: '', flagged: false }, sort: { key: 'date', asc: false },
      days: DEFAULT_DAYS, rows: ROW_STEP
    };
    state.days = state.win.days;

    // ---- URL state: q, agent, repo, session, flagged, sort, days ----
    var params = new URLSearchParams(location.search);
    state.f.q = (params.get('q') || '').toLowerCase();
    el.q.value = params.get('q') || '';
    state.f.agent = params.get('agent') || '';
    state.f.repo = params.get('repo') || '';
    state.f.flagged = params.get('flagged') === '1';
    state.sort = parseSort(params.get('sort'));
    state.session = params.get('session') || '';

    function syncURL() {
      var p = new URLSearchParams();
      if (el.q.value.trim()) p.set('q', el.q.value.trim());
      if (state.f.agent) p.set('agent', state.f.agent);
      if (state.f.repo) p.set('repo', state.f.repo);
      if (state.session) p.set('session', state.session);
      if (state.f.flagged) p.set('flagged', '1');
      if (!isDefaultSort(state.sort.key, state.sort.asc)) p.set('sort', sortParam(state.sort));
      if (state.days !== DEFAULT_DAYS) p.set('days', String(state.days));
      var qs = p.toString();
      try { history.replaceState(null, '', location.pathname + (qs ? '?' + qs : '')); } catch (e) {}
    }

    // ---- loading ----
    function load(qs) {
      el.view.classList.add('loading');
      return fetch('/prompts/data?' + qs, { headers: { Accept: 'application/json' } })
        .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
        .then(function (raw) { el.view.classList.remove('loading'); return normalize(raw); })
        .catch(function (e) {
          el.view.classList.remove('loading');
          el.empty.style.display = 'block';
          el.empty.textContent = 'Could not load prompts (' + e.message + '). Is sentientd running?';
          throw e;
        });
    }
    function setDays(days) {
      state.days = days;
      state.rows = ROW_STEP;
      load('days=' + days).then(function (w) { state.win = w; render(); }, function () {});
    }
    function setSession(id) {
      state.session = id; state.rows = ROW_STEP;
      if (!id) { state.one = null; render(); return; }
      load('session=' + encodeURIComponent(id)).then(function (w) { state.one = w; render(); }, function () {});
    }
    function loadOlder() {
      var data = state.session && state.one ? state.one : state.win;
      var qs = (state.session ? 'session=' + encodeURIComponent(state.session) : 'days=' + state.days) + '&before=' + encodeURIComponent(new Date(data.oldest).toISOString());
      load(qs).then(function (w) { merge(data, w); render(); }, function () {});
    }

    // ---- drawing ----
    function fillSelect(sel, items, current, first) {
      var h = first ? '<option value="">' + esc(first) + '</option>' : '';
      items.forEach(function (it) {
        h += '<option value="' + esc(it.value) + '"' + (it.value === current && !it.disabled ? ' selected' : '') + (it.disabled ? ' disabled' : '') + '>' + esc(it.label) + '</option>';
      });
      sel.innerHTML = h;
    }
    function render() {
      var data = state.session && state.one ? state.one : state.win;
      var s = state.session && (data.sessions[state.session] || state.win.sessions[state.session]);
      // agent/repo that no longer fit the chosen session: back to all sessions
      if (state.session && s && !matches({ f: 0, x: '', b: '' }, s, { agent: state.f.agent, repo: state.f.repo })) { state.session = ''; state.one = null; }
      var v = build(state);

      fillSelect(el.agent, state.win.agents.map(function (a) { return { value: a, label: a }; }), state.f.agent, 'All agents');
      fillSelect(el.repo, state.win.repos.map(function (a) { return { value: a, label: a }; }), state.f.repo, 'All repos');
      fillSelect(el.session, sessionOptions(state.session && state.one ? state.one : state.win, state.session, state.f).map(function (o) {
        return { value: o.value, label: o.label, disabled: o.disabled };
      }), state.session, 'All sessions');
      el.sort.value = sortParam(state.sort);
      el.flag.setAttribute('aria-pressed', state.f.flagged ? 'true' : 'false');

      el.view.innerHTML = v.html;
      el.shown.textContent = v.shown;
      el.total.textContent = v.total;
      el.wlabel.textContent = windowLabel(state.days) + (state.win.widened ? ' (widened to the newest ' + state.win.prompts.length + ')' : '');
      el.wcount.textContent = '· ' + plural(state.win.prompts.length, 'prompt') + (state.win.more ? ' loaded, more available' : '');
      Array.prototype.forEach.call(el.wbar.querySelectorAll('[data-days]'), function (b) {
        b.setAttribute('aria-pressed', Number(b.getAttribute('data-days')) === state.days ? 'true' : 'false');
      });
      if (v.empty) {
        el.empty.style.display = 'block';
        el.empty.innerHTML = esc(v.empty) + (v.searchAll ? '<button class="chip" type="button" data-days="0" id="search-all">Search all time</button>' : '');
      } else {
        el.empty.style.display = 'none';
      }
      if (v.more) {
        el.more.hidden = false;
        el.moreBtn.textContent = v.more.reveal ? 'Show more' : 'Load older';
        el.moreText.textContent = v.more.text;
        el.moreBtn.onclick = v.more.reveal
          ? function () { state.rows += ROW_STEP; render(); }
          : loadOlder;
      } else {
        el.more.hidden = true;
      }
      syncURL();
    }

    function refilter() {
      state.f.q = el.q.value.trim().toLowerCase();
      state.rows = ROW_STEP;
      render();
    }

    // ---- events ----
    el.q.addEventListener('input', refilter);
    el.agent.addEventListener('change', function () { state.f.agent = el.agent.value; refilter(); });
    el.repo.addEventListener('change', function () { state.f.repo = el.repo.value; refilter(); });
    el.session.addEventListener('change', function () { setSession(el.session.value); });
    el.sort.addEventListener('change', function () { state.sort = parseSort(el.sort.value); state.rows = ROW_STEP; render(); });
    el.flag.addEventListener('click', function () { state.f.flagged = !state.f.flagged; refilter(); });
    document.addEventListener('click', function (e) {
      var t = e.target;
      var head = t.closest && t.closest('button.sort');
      if (head) { state.sort = nextSort(state.sort, head.getAttribute('data-sort')); state.rows = ROW_STEP; render(); return; }
      var days = t.closest && t.closest('[data-days]');
      if (days) { e.preventDefault(); setDays(Number(days.getAttribute('data-days'))); return; }
      var tr = t.closest && t.closest('.prompts tbody tr[data-href]');
      if (tr && !(t.closest && t.closest('a'))) location.href = tr.getAttribute('data-href');
    });

    if (state.session) setSession(state.session);
    if (params.get('days') !== null && Number(params.get('days')) !== state.win.days) setDays(Number(params.get('days')));
    else render();
  }

  return api;
});
