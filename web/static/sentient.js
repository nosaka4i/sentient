// sentient web UI: theme toggle and clickable prompt rows (from the mockup).
(function () {
  var root = document.documentElement;
  var btn = document.getElementById('theme');
  if (btn) {
    btn.addEventListener('click', function () {
      var cur = root.getAttribute('data-theme');
      if (!cur) cur = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
      var next = cur === 'light' ? 'dark' : 'light';
      root.setAttribute('data-theme', next);
      try { localStorage.setItem('sentient-theme', next); } catch (e) {}
    });
  }

  // prompt page: show/hide the trace, expand/collapse the reply
  var tBtn = document.getElementById('trace-toggle');
  var tBody = document.getElementById('trace-body');
  var tCard = document.getElementById('trace');
  if (tBtn && tBody) {
    var setTrace = function (open) {
      tBody.hidden = !open;
      if (tCard) tCard.classList.toggle('collapsed', !open);
      tBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
      tBtn.querySelector('.lbl-txt').textContent = open ? tBtn.dataset.hide : tBtn.dataset.show;
    };
    tBtn.addEventListener('click', function () { setTrace(tBody.hidden); });
    // a tile links to #trace: open it
    document.querySelectorAll('a[href="#trace"]').forEach(function (a) {
      a.addEventListener('click', function () { setTrace(true); });
    });
  }
  var rBtn = document.getElementById('reply-toggle');
  var rBody = document.getElementById('reply-body');
  if (rBtn && rBody) {
    rBtn.addEventListener('click', function () {
      var clamped = rBody.classList.toggle('clamped');
      rBtn.textContent = clamped ? 'more' : 'less';
      rBtn.setAttribute('aria-expanded', clamped ? 'false' : 'true');
    });
  }

  // filter selects apply as soon as they change
  document.querySelectorAll('select[data-autosubmit]').forEach(function (sel) {
    sel.addEventListener('change', function () { sel.form.submit(); });
  });

  // whole prompt row is clickable
  document.querySelectorAll('.prompts tbody tr[data-href]').forEach(function (tr) {
    tr.addEventListener('click', function (e) {
      if (e.target.closest('a')) return;
      location.href = tr.getAttribute('data-href');
    });
  });
})();
