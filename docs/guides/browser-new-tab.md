# Using the legacy page locally

The current server-rendered page is a local-development aid at `http://localhost:8080`; it is not the
production browser client. Production starts in API bearer-token mode, where HTML routes intentionally
return 401. Revisit the setup below after a browser client and passkey/session flow have been implemented.

| Browser | How |
|---------|-----|
| **Vivaldi** | Settings → Tabs → *New Tab Page* → *Specific Page* → enter the URL. Optionally Settings → General → *Homepage* → same URL. |
| **Arc** | Arc opens the command bar on ⌘T; there is no configurable new‑tab page. Options: (1) pin zorgscope as the first pinned tab in your main Space (it stays open and refreshes itself); (2) Settings → General → *Little Arc*/Startup: open zorgscope on launch; (3) if you insist on ⌘T behaviour, the Chrome extension "New Tab Redirect" works in Arc — discouraged, not needed. |
| **Firefox** | Settings → Home → *Homepage and new windows* → Custom URL. For *New tabs* Firefox needs the extension "New Tab Override" (set the URL there); alternatively use the homepage + ⌘⇧H. |
| **Safari** | Settings → General → *New tabs open with: Homepage*, *Homepage:* the URL. |
| **Chromium/Chrome/Edge/Brave** | Settings → On startup → *Open a specific page* for launch; new‑tab redirect only via extension. |

Locally, the browser tab title shows the number of attention items, e.g. `(3) zorgscope`.
