// Sessions page: the filters are a plain GET form; this just submits it when
// a menu changes, so no Apply button is needed. Without script, Apply shows.
(function () {
  var form = document.getElementById('filters');
  if (!form) return;
  form.querySelectorAll('select').forEach(function (sel) {
    sel.addEventListener('change', function () { form.submit(); });
  });
  // clickable rows (the session id link inside still works on its own)
  document.addEventListener('click', function (e) {
    var tr = e.target.closest && e.target.closest('.sessions tbody tr[data-href]');
    if (tr && !e.target.closest('a')) location.href = tr.getAttribute('data-href');
  });
})();
