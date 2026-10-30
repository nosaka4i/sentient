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

  // prompt page: drawers (the trace, the files changed) open and close from
  // their button; a tile linking to #<name> opens the drawer
  ['trace', 'files'].forEach(function (name) {
    var btn = document.getElementById(name + '-toggle');
    var body = document.getElementById(name + '-body');
    var card = document.getElementById(name);
    if (!btn || !body) return;
    var set = function (open) {
      body.hidden = !open;
      if (card) card.classList.toggle('collapsed', !open);
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      btn.querySelector('.lbl-txt').textContent = open ? btn.dataset.hide : btn.dataset.show;
    };
    btn.addEventListener('click', function () { set(body.hidden); });
    document.querySelectorAll('a[href="#' + name + '"]').forEach(function (a) {
      a.addEventListener('click', function () { set(true); });
    });
  });
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
