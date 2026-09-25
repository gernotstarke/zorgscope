"""Generates docs/concepts/radar-prototype.svg — throwaway prototype, fake data."""
import math, random, textwrap, zlib
from html import escape

random.seed(42)
W, H = 1560, 1000
CX, CY, R = 580, 500, 430
R0 = 34  # inner "approach" disc
MAXD = 730  # two years at the rim
PERIOD = 8  # seconds per sweep, like the logo's idle arc

repos = [
    ("arc42-template", "slate", "arc42/arc42-template"),
    ("arc42.org", "navy", "arc42/arc42.org-site"),
    ("arc42.de", "navy", "arc42/arc42.de-site"),
    ("quality", "plum", "arc42/quality.arc42.org-site"),
    ("docs", "blue", "arc42/docs.arc42.org-site"),
    ("faq", "teal", "arc42/faq.arc42.org-site"),
    ("examples", "umber", "arc42/examples.arc42.org-site"),
    ("trainings", "rose", "arc42/trainings.arc42.org-site"),
    ("generator", "slate", "arc42/arc42-generator"),
    ("zorgscope", "slate", "gernotstarke/zorgscope"),
]

issue_titles = [
    "Broken link in section 5 building block view", "Typo in quality scenario QS-12",
    "Add German translation for chapter 9", "Search returns no results for umlauts",
    "Improve contrast of footer links", "Example for runtime view is outdated",
    "Clarify difference between constraints and conventions", "Mobile menu does not close",
    "Add example for deployment view with Kubernetes", "Missing alt text on diagrams",
    "Sitemap lists draft pages", "Glossary term 'stakeholder' duplicated",
    "Quality attribute 'maintainability' needs examples", "RSS feed is empty",
    "Template export to AsciiDoc drops tables", "FAQ entry C-4 contradicts template help",
]
pr_titles = [
    "Bump jekyll from 4.3.2 to 4.3.4", "Bump nokogiri from 1.16.2 to 1.16.5",
    "Fix typos in chapter 8", "Add Dutch translation of template",
    "Rework navigation for small screens", "Bump rexml from 3.2.6 to 3.3.9",
    "Update training dates for 2027", "Replace deprecated GitHub action",
    "Add quality requirement: energy efficiency", "Radar view prototype",
]
authors = ["dependabot", "rdmueller", "bitsmuggler", "jschwarzwalder", "mkorb", "ellen-k", "p-ahrens"]
labels_pool = ["bug", "documentation", "enhancement", "good first issue", "translation", "dependencies"]

def ago(d):
    if d < 1: return f"{max(1, int(d*24))} h ago"
    if d < 14: return f"{int(d)} d ago"
    if d < 60: return f"{int(d/7)} wk ago"
    if d < 365: return f"{int(d/30)} mo ago"
    return f"{d/365:.1f} yr ago"

def radius(days):
    return R0 + (R - R0 - 10) * math.log1p(days) / math.log1p(MAXD)

items = []
num = {r[2]: random.randint(40, 380) for r in repos}
for i, (short, hue, full) in enumerate(repos):
    for _ in range(random.randint(2, 7)):
        pr = random.random() < 0.42
        num[full] += random.randint(1, 9)
        updated = min(MAXD, random.expovariate(1 / 70))
        created = updated + random.expovariate(1 / 120)
        title = random.choice(pr_titles if pr else issue_titles)
        author = "dependabot" if title.startswith("Bump") else random.choice(authors[1:])
        sec = pr and title.startswith("Bump") and random.random() < 0.5
        if sec:
            author = "dependabot"
        needs = pr and not sec and random.random() < 0.35
        items.append(dict(
            repo=full, short=short, hue=hue, idx=i, pr=pr, n=num[full], title=title,
            author=author, created=created, updated=updated, sec=sec, needs=needs,
            labels=random.sample(labels_pool, random.randint(0, 2)) if not sec else ["dependencies", "security"],
            adv=f"GHSA-{random.randint(1000,9999)}-x{random.randint(10,99)}q" if sec else None,
        ))
# make sure the demo shows what it is meant to show
for k in (5, 20, 33):
    items[k].update(pr=True, sec=True, needs=False, author="dependabot", title="Bump rexml from 3.2.6 to 3.3.9", labels=["dependencies", "security"], adv=f"GHSA-{4000+k}-vg7j")
for k in (8, 27, 41):
    items[k].update(pr=True, sec=False, needs=False, author="dependabot", title="Bump jekyll from 4.3.2 to 4.3.4", labels=["dependencies"], adv=None)
items[3].update(needs=True, pr=True, updated=0.3, title="Radar view prototype", author="bitsmuggler")
items[11].update(needs=True, pr=True, updated=2.5)
for it in items:
    it["dep"] = it["author"] == "dependabot" and not it["sec"]
    if it["dep"] or it["sec"]:
        it["needs"] = False

sector = 360 / len(repos)
def pos(it):
    h = (zlib.crc32(f"{it['repo']}#{it['n']}".encode()) & 0xffff) / 0xffff
    ang = it["idx"] * sector + 4 + h * (sector - 8)  # degrees, 0 = north, clockwise
    r = radius(it["updated"])
    a = math.radians(ang - 90)
    return ang, CX + r * math.cos(a), CY + r * math.sin(a)

def polar(r, ang):
    a = math.radians(ang - 90)
    return CX + r * math.cos(a), CY + r * math.sin(a)

out = []
w = out.append
w(f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" font-family="ui-monospace, SFMono-Regular, Menlo, monospace" role="img" aria-labelledby="t d">
<title id="t">zorgscope radar — prototype</title>
<desc id="d">Throwaway prototype with fake data. Direction = repository, distance from centre = time since last activity (log scale). Circle = issue, diamond = pull request. Hover a blip for its data block; click to pin it.</desc>
<style>
  svg {{
    --bg: #eef3f0; --scope: #f8fbf9; --ring: #b7cbbf; --ring-text: #56705f; --text: #1c1e21; --muted: #5b6169;
    --sweep: #2e9e5b; --needs: #2f5bd7; --sec: #b3261e; --sec-dark: #6d1611; --dep: #b87400; --dep-dark: #4a3000; --panel: #ffffff; --panel-border: #cfdcd4; --link: #2f5bd7;
    --navy: #2b3a57; --blue: #0e4f80; --plum: #682d63; --teal: #1b5648; --umber: #3a332b; --rose: #a04c5e; --slate: #414a56;
  }}
  @media (prefers-color-scheme: dark) {{ svg {{
    --bg: #0b1210; --scope: #0f1a16; --ring: #1f4434; --ring-text: #4f9a74; --text: #e7e9ec; --muted: #9aa1ab;
    --sweep: #b6f36a; --needs: #7fa2ff; --sec: #ff5a47; --sec-dark: #5a120e; --dep: #f0b43c; --dep-dark: #3a2600; --panel: #131c19; --panel-border: #274b3b; --link: #7fa2ff;
    --navy: #8f9dbd; --blue: #6fb0e3; --plum: #c58cc0; --teal: #8fd0bd; --umber: #b9ab98; --rose: #de93a3; --slate: #a3acb2;
  }} }}
  .bg {{ fill: var(--bg); }}
  .scope {{ fill: var(--scope); stroke: var(--ring); stroke-width: 2; }}
  .ring {{ fill: none; stroke: var(--ring); stroke-width: 1; }}
  .edge {{ stroke: var(--h); stroke-width: 1.2; stroke-dasharray: 1 6; stroke-linecap: round; opacity: .45; }}
  .ringlabel {{ fill: var(--ring-text); font-size: 12px; }}
  .sitelabel {{ fill: var(--text); font-size: 14px; font-weight: 600; }}
  .rim {{ fill: none; stroke-width: 7; }}
  .centre {{ fill: none; stroke: var(--sweep); stroke-width: 1.5; opacity: .6; }}

  .hue-navy {{ --h: var(--navy); }} .hue-blue {{ --h: var(--blue); }} .hue-plum {{ --h: var(--plum); }}
  .hue-teal {{ --h: var(--teal); }} .hue-umber {{ --h: var(--umber); }} .hue-rose {{ --h: var(--rose); }}
  .hue-slate {{ --h: var(--slate); }}
  .rim {{ stroke: var(--h); }}
  .mark {{ fill: var(--h); stroke: var(--scope); stroke-width: 1.5; }}

  /* the sweep: one beam plus afterglow, rotating about the centre */
  .sweep {{ transform-origin: {CX}px {CY}px; animation: spin {PERIOD}s linear infinite; }}
  @keyframes spin {{ to {{ transform: rotate(360deg); }} }}
  .beam {{ stroke: var(--sweep); stroke-width: 2; }}
  .glow {{ fill: url(#glow); }}
  #glow stop {{ stop-color: var(--sweep); }}

  /* every blip flashes as the beam passes it; the delay is its bearing, computed server-side */
  .blip .mark {{ animation: flash {PERIOD}s linear infinite; }}
  @keyframes flash {{ 0% {{ opacity: 1; }} 2% {{ opacity: 1; filter: brightness(1.8); }} 60% {{ opacity: var(--rest); }} 100% {{ opacity: 1; }} }}

  .blip {{ cursor: pointer; outline: none; }}
  .blip .hit {{ fill: transparent; }}
  .halo {{ fill: none; stroke: var(--needs); stroke-width: 2; }}
  .halo-pulse {{ fill: none; stroke: var(--needs); stroke-width: 2; transform-box: fill-box; transform-origin: center; animation: pulse 2s ease-out infinite; }}
  @keyframes pulse {{ from {{ transform: scale(1); opacity: .8; }} to {{ transform: scale(2.2); opacity: 0; }} }}
  .tape {{ fill: none; stroke-width: 6; }}
  .tape-sec {{ stroke: url(#tape-sec); }} .tape-dep {{ stroke: url(#tape-dep); stroke-width: 5; }}
  #tape-sec .a {{ fill: var(--sec); }} #tape-sec .b {{ fill: var(--sec-dark); }}
  #tape-dep .a {{ fill: var(--dep); }} #tape-dep .b {{ fill: var(--dep-dark); }}
  .sec-pulse {{ fill: none; stroke: var(--sec); stroke-width: 3; transform-box: fill-box; transform-origin: center; animation: pulse 1.4s ease-out infinite; }}
  .sec .mark, .dep .mark {{ stroke: var(--text); stroke-width: 1.5; }}
  .sectag {{ fill: var(--sec); font-size: 12px; font-weight: 800; }}
  .deptag {{ fill: var(--dep); font-size: 11px; font-weight: 700; }}
  .tag {{ fill: var(--needs); font-size: 11px; font-weight: 700; }}
  .blip:hover .mark, .blip:focus .mark, .blip:focus-within .mark {{ stroke: var(--text); stroke-width: 2.5; }}
  .blip .leader {{ display: none; pointer-events: none; stroke: var(--text); stroke-width: 1; }}

  /* the data block: hidden until the blip is hovered or pinned (focused by a click) */
  .card {{ display: none; }}
  .blip:hover .card, .blip:focus .card, .blip:focus-within .card {{ display: inline; }}
  .blip:hover .leader, .blip:focus .leader, .blip:focus-within .leader {{ display: inline; }}
  /* hovering another blip takes over the panel from a pinned one */
  svg:has(.blip:hover) .blip:not(:hover) .card,
  svg:has(.blip:hover) .blip:not(:hover) .leader {{ display: none; }}
  svg:has(.blip:hover, .blip:focus, .blip:focus-within) .legend {{ display: none; }}
  .panel {{ fill: var(--panel); stroke: var(--panel-border); stroke-width: 1.5; }}
  .card text, .legend text {{ fill: var(--text); font-size: 15px; }}
  .card .k {{ fill: var(--muted); font-size: 13px; }}
  .card .ttl {{ font-size: 18px; font-weight: 700; font-family: system-ui, sans-serif; }}
  .card .needs {{ fill: var(--needs); font-weight: 700; }}
  .card .sec {{ fill: var(--sec); font-weight: 700; }}
  .card .dep {{ fill: var(--dep); font-weight: 700; }}
  .card a text {{ fill: var(--link); text-decoration: underline; }}
  .legend .k {{ fill: var(--muted); font-size: 13px; }}
  .legend .h {{ font-weight: 700; font-size: 16px; }}

  @media (prefers-reduced-motion: reduce) {{
    .sweep, .blip .mark, .halo-pulse, .sec-pulse {{ animation: none; }}
    .halo-pulse {{ display: none; }}
  }}
</style>
<defs>
  <pattern id="tape-sec" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect class="a" width="4" height="8"/><rect class="b" x="4" width="4" height="8"/></pattern>
  <pattern id="tape-dep" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect class="a" width="4" height="8"/><rect class="b" x="4" width="4" height="8"/></pattern>
  <linearGradient id="glow" gradientUnits="userSpaceOnUse" x1="{CX}" y1="{CY - R}" x2="{polar(R, -40)[0]:.1f}" y2="{polar(R, -40)[1]:.1f}">
    <stop offset="0" stop-opacity=".35"/><stop offset="1" stop-opacity="0"/>
  </linearGradient>
</defs>
<rect class="bg" width="{W}" height="{H}"/>
<circle class="scope" cx="{CX}" cy="{CY}" r="{R}"/>
''')

# sector spokes, rim arcs and site labels
for i, (short, hue, full) in enumerate(repos):
    a0, a1 = i * sector, (i + 1) * sector
    x, y = polar(R, a0)
    for ea in (a0 + 1.2, a1 - 1.2):
        e0, e1 = polar(R0 + 4, ea), polar(R + 2, ea)
        w(f'<line class="edge hue-{hue}" x1="{e0[0]:.1f}" y1="{e0[1]:.1f}" x2="{e1[0]:.1f}" y2="{e1[1]:.1f}"/>')
    p0, p1 = polar(R + 6, a0 + 1.2), polar(R + 6, a1 - 1.2)
    w(f'<path class="rim hue-{hue}" d="M{p0[0]:.1f},{p0[1]:.1f} A{R+6},{R+6} 0 0 1 {p1[0]:.1f},{p1[1]:.1f}"/>')
    mid = (a0 + a1) / 2
    lx, ly = polar(R + 30, mid)
    anchor = "middle" if abs(math.sin(math.radians(mid))) < .3 else ("start" if math.sin(math.radians(mid)) > 0 else "end")
    w(f'<text class="sitelabel" x="{lx:.1f}" y="{ly + 5:.1f}" text-anchor="{anchor}">{short}</text>')

# range rings (log scale), labels along the 36° spoke gap
for days, lab in [(1, "1 d"), (7, "1 wk"), (30, "1 mo"), (182, "6 mo"), (365, "1 yr")]:
    r = radius(days)
    w(f'<circle class="ring" cx="{CX}" cy="{CY}" r="{r:.1f}"/>')
    w(f'<text class="ringlabel" x="{CX + 4}" y="{CY - r - 4:.1f}">{lab}</text>')
w(f'<circle class="centre" cx="{CX}" cy="{CY}" r="{R0}"/>')

# sweep
bx, by = polar(R, 0)
gx, gy = polar(R, -40)
w(f'<g class="sweep" aria-hidden="true"><path class="glow" d="M{CX},{CY} L{gx:.1f},{gy:.1f} A{R},{R} 0 0 1 {bx:.1f},{by:.1f} Z"/>'
  f'<line class="beam" x1="{CX}" y1="{CY}" x2="{bx:.1f}" y2="{by:.1f}"/></g>')

# panel frame + legend
PX, PY, PW, PH = 1150, 60, 380, 880
w(f'<rect class="panel" x="{PX}" y="{PY}" width="{PW}" height="{PH}" rx="12"/>')
lg = [f'<g class="legend"><text class="h" x="{PX+24}" y="{PY+42}">Radar</text>',
      f'<text class="k" x="{PX+24}" y="{PY+66}">Hover a blip for its data block,</text>',
      f'<text class="k" x="{PX+24}" y="{PY+84}">click to pin it.</text>']
y = PY + 130
def leg(shape, text, sub=None):
    global y
    lg.append(f'<g transform="translate({PX+36},{y})" class="hue-slate">{shape}</g><text x="{PX+62}" y="{y+5}">{text}</text>')
    if sub: lg.append(f'<text class="k" x="{PX+62}" y="{y+23}">{sub}</text>')
    y += 50 if sub else 38
leg('<circle class="mark" r="7"/>', "Issue")
leg('<path class="mark" d="M0,-9 L9,0 L0,9 L-9,0 Z"/>', "Pull request")
leg('<circle class="mark" r="7"/><circle class="halo" r="12"/>', "Needs you", "review requested from you")
leg('<g class="sec"><circle class="tape tape-sec" r="13"/><circle class="mark" r="8"/></g>', "Security", "cites a CVE or GHSA")
leg('<g class="dep"><circle class="tape tape-dep" r="12"/><circle class="mark" r="7"/></g>', "Dependency", "Dependabot, no advisory")
y += 10
lg.append(f'<text x="{PX+24}" y="{y}">Direction  = repository</text>'); y += 24
lg.append(f'<text x="{PX+24}" y="{y}">Distance   = time since</text>'); y += 20
lg.append(f'<text x="{PX+24}" y="{y}">             last activity</text>'); y += 20
lg.append(f'<text class="k" x="{PX+24}" y="{y}">centre = just now, rim = 2 years</text>'); y += 40
n_needs = sum(i["needs"] for i in items); n_sec = sum(i["sec"] for i in items); n_dep = sum(i["dep"] for i in items)
n_pr = sum(i["pr"] for i in items)
lg.append(f'<text x="{PX+24}" y="{y}">{len(items)} open · {n_pr} PRs · {len(items)-n_pr} issues</text>'); y += 24
lg.append(f'<text x="{PX+24}" y="{y}"><tspan class="needs" fill="var(--needs)" font-weight="700">{n_needs} need you</tspan> · <tspan fill="var(--sec)" font-weight="700">{n_sec} security</tspan> · <tspan fill="var(--dep)" font-weight="700">{n_dep} dependency</tspan></text>')
lg.append('</g>')
w("\n".join(lg))

# blips: draw calm ones first, needs-you last so they sit on top
items.sort(key=lambda it: (it["needs"], it["dep"], it["sec"]))
for it in items:
    ang, x, y = pos(it)
    rest = 1 if it["sec"] or it["dep"] else max(.35, 1 - it["updated"] / MAXD * 0.9)
    k = 11 if it["sec"] else 9
    delay = ang / 360 * PERIOD
    label = f'{"Pull request" if it["pr"] else "Issue"} {it["short"]} #{it["n"]}: {it["title"]}'
    mark = (f'<path class="mark" d="M{x:.1f},{y-k:.1f} l{k},{k} l-{k},{k} l-{k},-{k} Z"' if it["pr"]
            else f'<circle class="mark" cx="{x:.1f}" cy="{y:.1f}" r="{k-2}"')
    mark += f' style="animation-delay:{delay - PERIOD:.2f}s;--rest:{rest:.2f}"/>'
    g = [f'<g class="blip hue-{it["hue"]}{" sec" if it["sec"] else ""}{" dep" if it["dep"] else ""}" tabindex="0" role="button" aria-label="{escape(label)}">',
         f'<circle class="hit" cx="{x:.1f}" cy="{y:.1f}" r="14"/>']
    if it["needs"]:
        g.append(f'<circle class="halo-pulse" cx="{x:.1f}" cy="{y:.1f}" r="12"/><circle class="halo" cx="{x:.1f}" cy="{y:.1f}" r="12"/>')
    if it["sec"]:
        g.append(f'<circle class="sec-pulse" cx="{x:.1f}" cy="{y:.1f}" r="15"/><circle class="tape tape-sec" cx="{x:.1f}" cy="{y:.1f}" r="15"/>')
        g.append(f'<text class="sectag" x="{x+20:.1f}" y="{y-12:.1f}">⚠ #{it["n"]}</text>')
    if it["dep"]:
        g.append(f'<circle class="tape tape-dep" cx="{x:.1f}" cy="{y:.1f}" r="13"/>')
    g.append(mark)
    if it["needs"]:
        g.append(f'<text class="tag" x="{x+15:.1f}" y="{y-10:.1f}">#{it["n"]}</text>')
    g.append(f'<line class="leader" x1="{x:.1f}" y1="{y:.1f}" x2="{PX}" y2="{PY+60}" stroke-dasharray="3 4"/>')
    # data block
    c = [f'<g class="card">',
         f'<text class="k" x="{PX+24}" y="{PY+40}">{"PULL REQUEST" if it["pr"] else "ISSUE"} · {escape(it["repo"])}</text>',
         f'<text class="k" x="{PX+24}" y="{PY+60}">#{it["n"]}</text>']
    ty = PY + 96
    for line in textwrap.wrap(it["title"], 30):
        c.append(f'<text class="ttl" x="{PX+24}" y="{ty}">{escape(line)}</text>'); ty += 24
    ty += 12
    if it["needs"]:
        c.append(f'<text class="needs" x="{PX+24}" y="{ty}">▲ Needs you: review requested</text>'); ty += 26
    if it["sec"]:
        c.append(f'<text class="sec" x="{PX+24}" y="{ty}">⚠ Security: {it["adv"]}</text>'); ty += 26
    if it["dep"]:
        c.append(f'<text class="dep" x="{PX+24}" y="{ty}">◆ Dependency update</text>'); ty += 26
    for k, v in [("author", it["author"]), ("opened", ago(it["created"])), ("activity", ago(it["updated"])),
                 ("labels", ", ".join(it["labels"]) or "—")]:
        c.append(f'<text x="{PX+24}" y="{ty}"><tspan class="k">{k:<9}</tspan> {escape(v)}</text>'); ty += 24
    ty += 8
    c.append(f'<text class="k" x="{PX+24}" y="{ty}">Summary</text>'); ty += 22
    for line in textwrap.wrap("Lorem ipsum stands in for the first 200 characters of the item body, which the dashboard already carries as Summary.", 33)[:4]:
        c.append(f'<text x="{PX+24}" y="{ty}">{line}</text>'); ty += 21
    ty += 20
    c.append(f'<a href="https://github.com/{it["repo"]}/{"pull" if it["pr"] else "issues"}/{it["n"]}" target="_blank"><text x="{PX+24}" y="{ty}">Open on GitHub ↗</text></a>')
    c.append('</g>')
    g += c
    g.append('</g>')
    w("".join(g))

w('</svg>\n')
open("/Users/gernotstarke/projects/privat/zorgscope/docs/concepts/radar-prototype.svg", "w").write("\n".join(out))
print(len(items), "items")
