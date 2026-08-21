// The header mark's refreshing state, brought forward to the moment the button is pressed.
//
// Everything else about the orbit is CSS and a server-rendered data-state attribute, and the page
// is complete without this file (FR-1.3 AC3). This covers the one gap the server cannot: "Refresh
// now" runs the entire refresh inside its own request, so between the press and the redirect the
// browser sits on an unchanged page for as long as the run takes — which on eight repositories is
// not a moment. The mark says what is happening for that whole time.
//
// Nothing here clears the state on success. The response to the form is a fresh document, and its
// mark is rendered from what the run actually did — so the regular orbit resumes by page load
// rather than by a timer this script would have to keep honest.
(() => {
  "use strict";

  const orbit = document.querySelector("[data-orbit]");
  const form = document.querySelector("[data-orbit-trigger]");
  if (!orbit || !form) {
    return;
  }

  const button = form.querySelector("button[type=submit]");
  // What the server rendered, kept so that a page restored from the back/forward cache can be put
  // back the way it was drawn. See the pageshow handler below.
  const rendered = { state: orbit.dataset.state, label: button ? button.textContent : "" };

  form.addEventListener("submit", () => {
    orbit.dataset.state = "refreshing";
    if (!button) {
      return;
    }

    // Deferred by a tick on purpose. Disabling a submit button from inside its own submit handler
    // is one way to cancel the submission in some browsers, and a refresh button that stops
    // refreshing is a worse bug than a double click. By the time this runs the submission is
    // under way and the button can safely say so: a second press would only earn the 409 the
    // server answers a concurrent run with.
    window.setTimeout(() => {
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
      button.textContent = "Refreshing…";
    }, 0);
  });

  // Going back to this page after a refresh restores it from the back/forward cache exactly as it
  // was left — mid-refresh, with the button disabled. Without this the control on the restored
  // page is dead, and nothing on it explains why.
  window.addEventListener("pageshow", (event) => {
    if (!event.persisted) {
      return;
    }
    orbit.dataset.state = rendered.state;
    if (button) {
      button.disabled = false;
      button.removeAttribute("aria-busy");
      button.textContent = rendered.label;
    }
  });
})();
