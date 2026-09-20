// The visitor's own last list, kept in localStorage so that a cold Machine has something to show
// while it asks GitHub (ADR-0013). It is a placeholder and never a source of truth: the page that
// arrives replaces it whole, and nothing here merges anything — the server already does that, and
// it is the only party that knows which repositories failed.
//
// Everything is wrapped in try/catch. localStorage throws rather than returning null in a Safari
// private window and wherever site data is blocked, and a placeholder that cannot be read must
// degrade to the plain wait page rather than take the page down with it.
(function () {
  "use strict";

  var KEY = "zorgscope.cache.v1";
  // Seven days. Safari's Intelligent Tracking Prevention evicts script-writable storage after
  // seven days of browser non-use regardless of what we ask for, so a longer expiry here would be
  // a promise the platform does not keep.
  var MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;

  function epoch() {
    return document.documentElement.getAttribute("data-cache-epoch") || "";
  }

  function read() {
    try {
      var raw = window.localStorage.getItem(KEY);
      if (!raw) { return null; }
      var v = JSON.parse(raw);
      // The epoch is the revocation: a stored list minted under a client secret that has since
      // been rotated must not be shown, whatever else it says (FR-8.3 AC4).
      if (!v || v.epoch !== epoch() || !v.html) { return null; }
      if (!v.savedAt || (Date.now() - v.savedAt) > MAX_AGE_MS) { return null; }
      return v;
    } catch (e) { return null; }
  }

  function clear() {
    try { window.localStorage.removeItem(KEY); } catch (e) { /* nothing to undo */ }
  }

  // Save whatever the server just rendered, verbatim. Storing the server's own HTML is what keeps
  // a second renderer out of the browser: this file never builds markup, it moves a string.
  function save() {
    var items = document.getElementById("items");
    if (!items) { return; }
    // A list drawn during a fetch carries the refresh poll. Storing that would restore a
    // placeholder that starts polling on a page whose own poll is already running.
    if (items.querySelector(".refreshing")) { return; }
    var stamp = document.querySelector("[data-fetched-at]");
    try {
      window.localStorage.setItem(KEY, JSON.stringify({
        epoch: epoch(),
        savedAt: Date.now(),
        fetchedAt: stamp ? stamp.getAttribute("data-fetched-at") : "",
        html: items.outerHTML
      }));
    } catch (e) { /* a full or blocked store simply means no placeholder next time */ }
  }

  function restore() {
    var mount = document.getElementById("placeholder");
    var v = read();
    if (!mount || !v) { return; }

    var note = document.createElement("p");
    note.className = "placeholder-note";
    note.setAttribute("role", "status");
    // textContent, not innerHTML: fetchedAt came back out of storage and is treated as text.
    note.textContent = v.fetchedAt
      ? "Showing your last view, fetched " + v.fetchedAt + ", while GitHub is asked."
      : "Showing your last view while GitHub is asked.";
    mount.appendChild(note);

    // v.html is the server's own rendered fragment, escaped by html/template when it was written.
    var holder = document.createElement("div");
    holder.innerHTML = v.html;
    // The restored copy must not keep the live list's id: the wait page's poll swaps main, and two
    // #items on one document is invalid and makes getElementById ambiguous.
    var restored = holder.firstElementChild;
    if (restored) { restored.removeAttribute("id"); }
    mount.appendChild(holder);
  }

  // Exactly one of the three applies to any page: the sign-in page clears, the wait page restores,
  // and a page with a list saves.
  if (document.getElementById("signed-out")) {
    clear();
  } else if (document.getElementById("placeholder")) {
    restore();
  } else {
    save();
  }
})();
