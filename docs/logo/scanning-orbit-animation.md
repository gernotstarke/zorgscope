# Scanning-orbit logo animation

Keep the existing logo static and place a separate SVG circle around it. The circle contains a
subtle track plus one rounded lime arc. In the idle state, rotate the arc once every 8 seconds. While
the application is polling, increase the arc to a 3.8 px visual stroke and rotate it once every
1.05 seconds. At the same time, emit a translucent reddish ring from the logo’s center: scale it from
roughly 0.86 to 1.86 and fade it out over the same 1.05-second cycle. Animate only `transform` and
`opacity`.

Drive the component with one state attribute such as `data-state="idle|refreshing|success|stale"`.
Set `refreshing` when the real polling request starts—not from a decorative timer—and clear it on
every completion path. On success, decelerate the arc to twelve o’clock over 650 ms with
`cubic-bezier(0.16, 1, 0.3, 1)`, emit one lime confirmation pulse, then return to idle. On failure or
stale data, stop the arc and make it amber; always show adjacent status text so color is not the only
signal.

The production JavaScript may use the Web Animations API to retain the arc’s current angle when
changing speed and to perform the final deceleration. Keep CSS and JavaScript external for the site’s
CSP. Under `prefers-reduced-motion: reduce`, disable rotation and scaling: show a stationary thick arc
and a steady reddish center ring while polling, while preserving the textual status update.
