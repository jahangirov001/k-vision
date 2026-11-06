// The start screen's background: AI meets Kubernetes. A slowly drifting neural
// network whose nodes are Kubernetes-style hexagons (pods) clustered into groups
// (nodes), with signals pulsing along the connections, behind a large faint helm
// wheel that turns. Drawn on a canvas, so nothing is downloaded; it follows the
// theme and stops moving for prefers-reduced-motion or when the start screen is hidden.
"use strict";
(function (root) {
  let canvas, ctx, raf = 0, w = 0, h = 0, dpr = 1, nodes = [], links = [], pulses = [], t0 = 0;
  const still = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
  const cssVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
  const dark = () => document.documentElement.dataset.theme === "dark";

  function palette() {
    const d = dark();
    return {
      accent: cssVar("--accent") || (d ? "#7c9dff" : "#2459d6"),
      cyan: d ? "#5fd4e0" : "#0f8fa3",
      violet: d ? "#c792ea" : "#7b4bd1",
      lineA: d ? 0.22 : 0.16,
      nodeA: d ? 0.85 : 0.6,
      wheelA: d ? 0.07 : 0.06,
      glow: d ? 18 : 10,
    };
  }

  function hex(x, y, r) {
    ctx.beginPath();
    for (let i = 0; i < 6; i++) {
      const a = Math.PI / 6 + (i * Math.PI) / 3;
      ctx.lineTo(x + r * Math.cos(a), y + r * Math.sin(a));
    }
    ctx.closePath();
  }

  // A few clusters of hexagon "pods" around "node" centres, joined like layers of a
  // neural network: dense inside a cluster, a few long links between clusters.
  function build() {
    nodes = []; links = []; pulses = [];
    const groups = Math.max(5, Math.round((w * h) / 210000));
    const centres = [];
    for (let g = 0; g < groups; g++) {
      // keep the middle (where the cards are) lighter: bias centres to the edges
      let x, y, tries = 0;
      do {
        x = Math.random() * w; y = Math.random() * h; tries++;
      } while (tries < 30 && Math.abs(x - w / 2) < w * 0.22 && y > h * 0.18 && y < h * 0.9);
      centres.push({ x, y, hue: g % 3 });
    }
    centres.forEach((c, gi) => {
      const k = 5 + Math.floor(Math.random() * 6);
      const start = nodes.length;
      nodes.push({ x: c.x, y: c.y, r: 9 + Math.random() * 4, hub: true, g: gi, hue: c.hue, ph: Math.random() * 6.28,
        vx: (Math.random() - 0.5) * 0.06, vy: (Math.random() - 0.5) * 0.06 });
      for (let i = 0; i < k; i++) {
        const a = Math.random() * 6.28, d = 40 + Math.random() * 110;
        nodes.push({ x: c.x + Math.cos(a) * d, y: c.y + Math.sin(a) * d, r: 3.5 + Math.random() * 3, g: gi, hue: c.hue,
          ph: Math.random() * 6.28, vx: (Math.random() - 0.5) * 0.12, vy: (Math.random() - 0.5) * 0.12 });
        links.push([start, nodes.length - 1]);
        if (i > 0 && Math.random() < 0.45) links.push([nodes.length - 2, nodes.length - 1]);
      }
    });
    // between clusters: each hub to its two nearest other hubs
    const hubs = nodes.map((n, i) => [n, i]).filter(([n]) => n.hub);
    for (const [a, ai] of hubs) {
      hubs.filter(([b]) => b !== a).sort(([b1], [b2]) => Math.hypot(b1.x - a.x, b1.y - a.y) - Math.hypot(b2.x - a.x, b2.y - a.y))
        .slice(0, 2).forEach(([, bi]) => { if (!links.some(([p, q]) => (p === bi && q === ai))) links.push([ai, bi]); });
    }
  }

  function spawnPulse() {
    if (!links.length) return;
    const l = links[Math.floor(Math.random() * links.length)];
    pulses.push({ l: Math.random() < 0.5 ? l : [l[1], l[0]], t: 0, v: 0.004 + Math.random() * 0.008 });
  }

  function frame(now) {
    const p = palette();
    const tt = (now - t0) / 1000;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    const hueCol = [p.accent, p.cyan, p.violet];

    // the helm wheel, large and faint, turning very slowly, lower right
    const R = Math.min(w, h) * 0.42, cx = w * 0.86, cy = h * 0.78, rot = still() ? 0 : tt * 0.02;
    ctx.save(); ctx.translate(cx, cy); ctx.rotate(rot);
    ctx.globalAlpha = p.wheelA; ctx.strokeStyle = p.accent; ctx.lineCap = "round";
    ctx.lineWidth = R * 0.06; ctx.beginPath(); ctx.arc(0, 0, R * 0.7, 0, 6.283); ctx.stroke();
    ctx.lineWidth = R * 0.05;
    for (let i = 0; i < 7; i++) { // Kubernetes' wheel has seven spokes
      const a = (i * 2 * Math.PI) / 7;
      ctx.beginPath(); ctx.moveTo(Math.cos(a) * R * 0.18, Math.sin(a) * R * 0.18); ctx.lineTo(Math.cos(a) * R, Math.sin(a) * R); ctx.stroke();
    }
    ctx.fillStyle = p.accent; ctx.beginPath(); ctx.arc(0, 0, R * 0.16, 0, 6.283); ctx.fill();
    ctx.restore();

    // drift
    if (!still()) for (const n of nodes) {
      n.x += n.vx; n.y += n.vy;
      if (n.x < -40 || n.x > w + 40) n.vx *= -1;
      if (n.y < -40 || n.y > h + 40) n.vy *= -1;
    }
    // links
    ctx.lineWidth = 1;
    for (const [a, b] of links) {
      const A = nodes[a], B = nodes[b];
      const g = ctx.createLinearGradient(A.x, A.y, B.x, B.y);
      g.addColorStop(0, hueCol[A.hue]); g.addColorStop(1, hueCol[B.hue]);
      ctx.strokeStyle = g; ctx.globalAlpha = A.hub && B.hub ? p.lineA * 0.8 : p.lineA;
      ctx.beginPath(); ctx.moveTo(A.x, A.y); ctx.lineTo(B.x, B.y); ctx.stroke();
    }
    // pulses: signals travelling along links, like activations through a network
    if (!still() && Math.random() < 0.18 && pulses.length < 40) spawnPulse();
    ctx.globalAlpha = 1;
    pulses = pulses.filter((q) => (q.t += q.v) < 1);
    for (const q of pulses) {
      const A = nodes[q.l[0]], B = nodes[q.l[1]];
      const x = A.x + (B.x - A.x) * q.t, y = A.y + (B.y - A.y) * q.t;
      ctx.shadowColor = hueCol[A.hue]; ctx.shadowBlur = p.glow;
      ctx.fillStyle = hueCol[A.hue]; ctx.globalAlpha = Math.sin(q.t * Math.PI) * 0.9;
      ctx.beginPath(); ctx.arc(x, y, 2.2, 0, 6.283); ctx.fill();
    }
    ctx.shadowBlur = 0;
    // nodes: hexagons (pods); hubs are bigger, outlined, and breathe
    for (const n of nodes) {
      const breathe = still() ? 1 : 1 + Math.sin(tt * 1.3 + n.ph) * 0.12;
      ctx.globalAlpha = p.nodeA * (n.hub ? 0.9 : 0.75);
      ctx.fillStyle = hueCol[n.hue];
      if (n.hub) {
        ctx.shadowColor = hueCol[n.hue]; ctx.shadowBlur = p.glow;
        hex(n.x, n.y, n.r * breathe); ctx.globalAlpha = p.nodeA * 0.25; ctx.fill();
        ctx.globalAlpha = p.nodeA; ctx.lineWidth = 1.6; ctx.strokeStyle = hueCol[n.hue]; ctx.stroke();
        ctx.shadowBlur = 0;
        hex(n.x, n.y, n.r * 0.38); ctx.fill();
      } else {
        hex(n.x, n.y, n.r * breathe); ctx.fill();
      }
    }
    ctx.globalAlpha = 1;
    if (!still()) raf = requestAnimationFrame(frame);
  }

  function resize() {
    if (!canvas) return;
    const r = canvas.getBoundingClientRect();
    dpr = Math.min(2, devicePixelRatio || 1);
    w = Math.max(1, r.width); h = Math.max(1, r.height);
    canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr);
    build();
  }

  function start(el) {
    canvas = el; ctx = canvas.getContext("2d");
    if (!ctx) return;
    stop();
    resize();
    t0 = performance.now();
    raf = requestAnimationFrame(frame);
  }
  function stop() { cancelAnimationFrame(raf); raf = 0; }
  function redraw() { if (canvas && !raf) raf = requestAnimationFrame(frame); }

  addEventListener("resize", () => { if (canvas && canvas.offsetParent !== null) { resize(); redraw(); } });
  root.KVHero = { start, stop, redraw };
})(window);
