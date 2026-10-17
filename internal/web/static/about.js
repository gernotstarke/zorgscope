// The about page's radar (FR-1.18). go, fly, htmx, github, cologne and zorg light up as blips when
// the sweep passes their bearing; on its second turn their letters spiral into the logo, the
// leftovers fall into the centre and come back out as the s and the p, which none of the six words
// has, and a ping goes out.
//
// A file of its own because the policy grants no 'unsafe-inline' (QS-4.4). Every value it sets goes
// through the CSSOM and the Web Animations API, which the policy allows; no style attribute is ever
// rendered. The page is complete without it: the logo, tagline and credit are the page's own text,
// and this script only hides the logo's letters while its own fly onto their places.
(() => {
  'use strict';

  const stage = document.getElementById('about-stage');
  const play = document.getElementById('about-play');
  if (!stage || !play || typeof stage.animate !== 'function') return;

  const logo = stage.querySelector('.about-logo');
  const targets = Array.from(logo.children);
  const bar = stage.querySelector('.about-bar');
  const fades = [stage.querySelector('.about-tagline'), stage.querySelector('.about-credit')];
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)');

  const WORDS = [
    ['go', '--about-go'], ['fly', '--about-fly'], ['htmx', '--about-htmx'],
    ['github', '--about-github'], ['cologne', '--about-cologne'], ['zorg', '--about-zorg'],
  ];
  // Clock bearings, in the order of WORDS: go at ten, fly at twelve, and on round.
  const BEARINGS = [300, 0, 60, 120, 180, 240];
  // Which word and letter becomes each letter of the logo; null is made from the leftovers.
  const FEED = [[5, 0], [0, 1], [5, 2], [3, 0], null, [4, 0], [4, 1], null, [4, 6]];
  const REV = 2200; // one turn of the sweep, ms
  const S = 0.42; // a word's size against the logo's

  let anims = [];
  let made = [];
  let seed = 42;

  // A small seeded generator, so every run tumbles the same way.
  const rnd = () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
  const tf = (cx, cy, w, h, rz, s) => `translate(${cx - w / 2}px, ${cy - h / 2}px) rotate(${rz}deg) scale(${s})`;

  // A token's colour as the browser resolves it, so light-dark() pairs arrive as plain colours the
  // animations can interpolate.
  const probe = document.createElement('span');
  const colour = (name) => {
    probe.style.color = `var(${name})`;
    stage.appendChild(probe);
    const c = getComputedStyle(probe).color;
    probe.remove();
    return c;
  };

  const animate = (el, frames, opts) => {
    const a = el.animate(frames, Object.assign({ fill: 'forwards', easing: 'linear' }, opts));
    anims.push(a);
    return a;
  };
  const add = (el) => {
    stage.appendChild(el);
    made.push(el);
    return el;
  };
  const glyph = (ch, c, fs) => {
    const el = document.createElement('span');
    el.className = 'about-ch';
    el.textContent = ch;
    el.setAttribute('aria-hidden', 'true');
    el.style.color = c;
    el.style.fontSize = fs + 'px';
    return add(el);
  };
  const measure = (text, fs) => {
    const row = document.createElement('div');
    row.className = 'about-measure';
    row.style.fontSize = fs + 'px';
    for (const ch of text) {
      const s = document.createElement('span');
      s.textContent = ch;
      row.appendChild(s);
    }
    stage.appendChild(row);
    const total = row.getBoundingClientRect().width;
    const letters = Array.from(row.children).map((s) => ({ ch: s.textContent, off: s.offsetLeft + s.offsetWidth / 2, w: s.offsetWidth, h: s.offsetHeight }));
    row.remove();
    return { total, letters };
  };

  function stop() {
    anims.forEach((a) => a.cancel());
    anims = [];
    made.forEach((el) => el.remove());
    made = [];
    stage.classList.remove('is-playing');
  }

  function run() {
    stop();
    seed = 42;
    stage.classList.add('is-playing');
    const sr = stage.getBoundingClientRect();
    const W = sr.width;
    const H = sr.height;
    const fs = parseFloat(getComputedStyle(logo).fontSize);
    const lr = logo.getBoundingClientRect();
    const C = { x: lr.left - sr.left + lr.width / 2, y: lr.top - sr.top + lr.height / 2 };
    // Where each logo letter ends: exactly where the page's own letter sits.
    const tg = targets.map((el) => {
      const r = el.getBoundingClientRect();
      return { ch: el.textContent, w: r.width, h: r.height, cx: r.left - sr.left + r.width / 2, cy: r.top - sr.top + r.height / 2, color: getComputedStyle(el).color };
    });

    const D = Math.min(W, H) * 0.92;
    const R = D * 0.4;
    const scope = add(document.createElement('div'));
    scope.className = 'about-scope';
    Object.assign(scope.style, { width: D + 'px', height: D + 'px', left: C.x - D / 2 + 'px', top: C.y - D / 2 + 'px' });
    stage.insertBefore(scope, stage.firstChild);
    const sweep = document.createElement('div');
    sweep.className = 'about-sweep';
    scope.appendChild(sweep);
    animate(scope, [{ opacity: 0, transform: 'scale(.85)' }, { opacity: 1, transform: 'scale(1)' }], { duration: 500, easing: 'ease-out', fill: 'both' });
    animate(sweep, [{ transform: 'rotate(0deg)' }, { transform: 'rotate(360deg)' }], { duration: REV, iterations: 2.25, delay: 200, fill: 'both' });

    // The six words on the ring, each appearing as the beam passes it.
    const src = [];
    WORDS.forEach(([text, token], wi) => {
      const m = measure(text, fs);
      const c = colour(token);
      const b = (BEARINGS[wi] * Math.PI) / 180;
      const hw = (m.total * S) / 2;
      const hh = (fs * S) / 2;
      const x = clamp(C.x + R * Math.sin(b), hw + 12, W - hw - 12);
      const y = clamp(C.y - R * Math.cos(b), hh + 10, H - hh - 10);
      const at = 200 + (BEARINGS[wi] / 360) * REV;
      m.letters.forEach((l, li) => {
        const L = { wi, li, w: l.w, h: l.h, cx: x + (l.off - m.total / 2) * S, cy: y, color: c, el: glyph(l.ch, c, fs) };
        animate(L.el, [
          { transform: tf(L.cx, L.cy, L.w, L.h, 0, S * 0.2), opacity: 0 },
          { offset: 0.35, transform: tf(L.cx, L.cy, L.w, L.h, 0, S * 1.3), opacity: 1 },
          { transform: tf(L.cx, L.cy, L.w, L.h, 0, S), opacity: 1 },
        ], { duration: 650, delay: at + li * 30, easing: 'ease-out', fill: 'both' });
        src.push(L);
      });
      ping(x, y, fs * 0.9, c, at);
    });

    // A letter's path into place: a spiral about the centre, turning clockwise at least `extra`
    // further than the straight way round, growing or shrinking as it goes.
    const spiral = (L, x1, y1, s0, s1, extra, opts, from, to, vanish) => {
      const a0 = Math.atan2(L.cy - C.y, L.cx - C.x);
      const r0 = Math.hypot(L.cx - C.x, L.cy - C.y);
      const a1 = Math.atan2(y1 - C.y, x1 - C.x);
      const r1 = Math.hypot(x1 - C.x, y1 - C.y);
      let da = a1 - a0;
      while (da <= 0) da += Math.PI * 2;
      da += extra;
      const N = 18;
      const frames = [];
      for (let k = 0; k <= N; k++) {
        const t = k / N;
        const e = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
        const a = a0 + da * e;
        const r = r0 + (r1 - r0) * e;
        const f = { offset: t, transform: tf(C.x + r * Math.cos(a), C.y + r * Math.sin(a), L.w, L.h, 360 * e, s0 + (s1 - s0) * e) };
        if (vanish) f.opacity = k === N ? 0 : 1;
        frames.push(f);
      }
      if (from) frames[0].color = from;
      if (to) frames[N].color = to;
      animate(L.el, frames, opts);
    };

    const T2 = 200 + REV * 1.1;
    const finals = [];
    let order = 0;
    FEED.forEach((f, i) => {
      if (!f) return;
      const L = src.find((s) => s.wi === f[0] && s.li === f[1]);
      L.used = true;
      spiral(L, tg[i].cx, tg[i].cy, S, 1, Math.PI * 0.6, { duration: 1500, delay: T2 + order++ * 90 }, L.color, tg[i].color, false);
      finals[i] = { el: L.el, t: tg[i] };
    });
    src.filter((L) => !L.used).forEach((L) => {
      spiral(L, C.x + 0.5, C.y, S, 0.05, Math.PI * 0.4, { duration: 1100, delay: T2 + rnd() * 300 }, null, null, true);
    });
    [4, 7].forEach((i, k) => {
      const t = tg[i];
      const L = { cx: C.x + 1, cy: C.y, w: t.w, h: t.h, el: glyph(t.ch, t.color, fs) };
      spiral(L, t.cx, t.cy, 0, 1, Math.PI, { duration: 1000, delay: T2 + 1200 + k * 120, fill: 'both' }, null, null, false);
      finals[i] = { el: L.el, t };
    });

    const TF = T2 + 2300;
    ping(C.x, C.y, D * 0.9, tg[2].color, TF - 200);
    animate(scope, [{ opacity: 1 }, { opacity: 0 }], { duration: 1400, delay: TF - 200 });
    finals.forEach(({ el, t }, i) => {
      animate(el, [
        { transform: tf(t.cx, t.cy, t.w, t.h, 0, 1) },
        { offset: 0.4, transform: tf(t.cx, t.cy - fs * 0.09, t.w, t.h, 0, 1.1) },
        { transform: tf(t.cx, t.cy, t.w, t.h, 0, 1) },
      ], { duration: 620, delay: TF + i * 70, easing: 'ease-in-out' });
    });
    animate(bar, [{ opacity: 0, transform: 'scaleX(0)' }, { opacity: 1, transform: 'scaleX(1)' }], { duration: 900, delay: TF + 150, easing: 'cubic-bezier(.65,0,.35,1)', fill: 'both' });
    fades.forEach((el, k) => {
      animate(el, [{ opacity: 0, transform: 'translateY(14px)' }, { opacity: 1, transform: 'translateY(0)' }], { duration: 800, delay: TF + 450 + k * 350, easing: 'cubic-bezier(.2,.8,.3,1)', fill: 'both' });
    });

    const mine = anims;
    Promise.all(mine.map((a) => a.finished)).then(() => {
      if (anims === mine) stop();
    }, () => {});
  }

  function ping(x, y, size, c, delay) {
    const p = add(document.createElement('span'));
    p.className = 'about-ping';
    Object.assign(p.style, { width: size + 'px', height: size + 'px', borderColor: c });
    animate(p, [
      { transform: `translate(${x - size / 2}px, ${y - size / 2}px) scale(.2)`, opacity: 0.8 },
      { transform: `translate(${x - size / 2}px, ${y - size / 2}px) scale(1)`, opacity: 0 },
    ], { duration: 1100, delay, easing: 'cubic-bezier(.2,.8,.3,1)' });
  }

  // A resize mid-flight would leave letters flying to the old places: jump to the end instead.
  let width = stage.getBoundingClientRect().width;
  window.addEventListener('resize', () => {
    const w = stage.getBoundingClientRect().width;
    if (Math.abs(w - width) < 2) return;
    width = w;
    anims.forEach((a) => a.finish());
  });

  play.hidden = false;
  play.addEventListener('click', run);
  if (!reduce.matches) run();
})();
