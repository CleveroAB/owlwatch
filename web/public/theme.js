// Resolve the theme before first paint so there is no flash of the wrong
// theme. Precedence: ?theme= URL param > stored choice > OS preference.
(function () {
  var param = new URLSearchParams(location.search).get('theme');
  var stored = null;
  try {
    stored = localStorage.getItem('owlwatch-theme');
  } catch (error) {
    // Storage may be unavailable; fall through to the OS preference.
  }
  var theme =
    param === 'light' || param === 'dark'
      ? param
      : stored === 'light' || stored === 'dark'
        ? stored
        : window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches
          ? 'light'
          : 'dark';
  document.documentElement.dataset.theme = theme;
})();
