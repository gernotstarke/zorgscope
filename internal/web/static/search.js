// Cmd-K or Ctrl-K focuses the search box and selects its text; Escape leaves it (FR-12.1 AC3).
// Nothing else: the search itself is a form the server answers.
(function () {
  "use strict";
  // Ctrl-K inside another text field is the kill-line macOS text fields honour, so it is left to
  // them; Cmd-K is ours everywhere.
  function typingElsewhere(el, box) {
    return !!el && el !== box &&
      (el.isContentEditable || el.tagName === "INPUT" || el.tagName === "TEXTAREA");
  }
  document.addEventListener("keydown", function (e) {
    var box = document.querySelector("input[data-search]");
    if (!box) { return; }
    if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
      if (!e.metaKey && typingElsewhere(document.activeElement, box)) { return; }
      e.preventDefault();
      box.focus();
      box.select();
    } else if (e.key === "Escape" && document.activeElement === box) {
      box.blur();
    }
  });
})();
