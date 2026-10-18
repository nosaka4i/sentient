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

  // whole prompt row is clickable
  document.querySelectorAll('.prompts tbody tr').forEach(function (tr) {
    tr.addEventListener('click', function (e) {
      if (e.target.closest('a')) return;
      location.href = tr.getAttribute('data-href');
    });
  });
})();
