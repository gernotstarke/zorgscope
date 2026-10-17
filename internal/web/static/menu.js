// The menu's two conveniences (FR-1.12 AC1): Escape closes it, and so does its close button,
// which is hidden until this runs. Opening, closing on the menu button and closing on a click
// outside the panel need no script at all: the menu is a <details>, and the dimmed page behind
// the panel is its summary. A file of its own because the policy grants no 'unsafe-inline'.
(function () {
  "use strict";
  var menu = document.querySelector("details[data-menu]");
  if (!menu) { return; }
  var button = menu.querySelector("summary");
  function close() {
    if (!menu.open) { return; }
    menu.open = false;
    button.focus();
  }
  var x = menu.querySelector("[data-menu-close]");
  if (x) {
    x.hidden = false;
    x.addEventListener("click", close);
  }
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { close(); }
  });
})();
