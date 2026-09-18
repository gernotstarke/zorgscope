// Cmd-K or Ctrl-K focuses the search box and selects its text; Escape leaves it (FR-12.1 AC3).
// Nothing else: the search itself is a form the server answers.
(function () {
  "use strict";
  document.addEventListener("keydown", function (e) {
    var box = document.querySelector("input[data-search]");
    if (!box) { return; }
    if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
      e.preventDefault();
      box.focus();
      box.select();
    } else if (e.key === "Escape" && document.activeElement === box) {
      box.blur();
    }
  });
})();
