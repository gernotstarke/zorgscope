// wait-for-ready polls zorgscope's /readyz until it answers 200 (or a deadline elapses) before the
// Playwright suite runs. Without this, `playwright`'s container only waits for `zorgscope`'s
// container to have *started* (deploy/compose.e2e.yml has no healthcheck-gated depends_on, and the
// zorgscope image is distroless — no shell, curl or wget inside it to run a Docker HEALTHCHECK
// against), not for it to have opened the SQLite store, parsed templates and bound the HTTP port.
// The very first `page.goto('/')` in tests/smoke.spec.ts could otherwise race that startup and fail
// with a connection error on a slow CI runner — flaky, not deterministic. Polling /readyz here (a
// tool already available in this Node-based Playwright image, so no new dependency) makes the wait
// explicit and bounded instead.
const base = process.env.BASE_URL ?? 'http://localhost:8080';
const url = `${base}/readyz`;
const deadlineMs = 60_000;
const intervalMs = 500;

async function waitForReady() {
  const deadline = Date.now() + deadlineMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (res.ok) {
        console.log(`wait-for-ready: ${url} is ready`);
        return;
      }
      lastErr = new Error(`status ${res.status}`);
    } catch (err) {
      lastErr = err;
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
  throw new Error(`wait-for-ready: ${url} did not become ready within ${deadlineMs}ms: ${lastErr}`);
}

waitForReady().catch((err) => {
  console.error(err.message);
  process.exit(1);
});
