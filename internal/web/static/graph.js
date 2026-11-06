// The resource map: one namespace drawn as the path traffic and configuration take
// through it, left to right --
//   Traffic (Ingress) → Services → Workloads (with their pods as dots) → Config & storage
// Arrows say what they are: an Ingress *routes* to a Service, a Service *selects* a
// workload's pods, a workload *mounts* or *reads* a ConfigMap, Secret or volume.
// Hovering (or clicking) a node highlights the chain it belongs to; double-click
// opens it. Built from SVG and DOM nodes only -- names come from the cluster and are
// set as text, never parsed as markup.
"use strict";
(function (root) {
  const NS = "http://www.w3.org/2000/svg";
  const COLS = [
    { title: "Traffic", hint: "Ingresses" },
    { title: "Services", hint: "what receives traffic" },
    { title: "Workloads", hint: "and their pods" },
    { title: "Config & storage", hint: "what they read and mount" },
  ];
  const W = 250, GAP = 92, PAD = 28, HEAD = 54, VGAP = 12;

  const sv = (tag, attrs = {}, ...kids) => {
    const e = document.createElementNS(NS, tag);
    for (const [k, v] of Object.entries(attrs)) if (v != null) e.setAttribute(k, v);
    for (const k of kids) if (k != null) e.append(k);
    return e;
  };
  const txt = (x, y, s, cls, extra = {}) => { const t = sv("text", { x, y, class: cls, ...extra }); t.textContent = s; return t; };
  const clip = (s, n) => (s && s.length > n ? s.slice(0, n - 1) + "…" : s || "");
  // Split a long resource name on its dashes into two lines that fit the card.
  function wrapName(name, max) {
    if (name.length <= max) return [name];
    const cut = name.lastIndexOf("-", max);
    if (cut > 8) return [name.slice(0, cut + 1), clip(name.slice(cut + 1), max)];
    return [name.slice(0, max), clip(name.slice(max), max)];
  }
  const NAME_MAX = 30;

  function nodeHeight(n) {
    let h = 46 + (wrapName(n.name, NAME_MAX).length - 1) * 16; // kind line + name line(s)
    if (n.info) h += 16;
    if (n.pods && n.pods.length) h += 10 + Math.ceil(n.pods.length / 20) * 11;
    if (n.badges && n.badges.length) h += 22;
    return h;
  }

  // Order nodes in each column so related ones sit level: workloads by the mean row of
  // the services that select them, config by the workloads that use it, and so on.
  function layout(g) {
    const cols = [[], [], [], []];
    for (const n of g.nodes) cols[n.col].push(n);
    // An empty column (no Ingress in this namespace) is dropped, not drawn as a blank lane.
    const shown = [0, 1, 2, 3].filter((i) => cols[i].length);
    const slot = {}; shown.forEach((c, i) => (slot[c] = i));
    const out = {}, inn = {};
    for (const e of g.edges) { (out[e.from] = out[e.from] || []).push(e.to); (inn[e.to] = inn[e.to] || []).push(e.from); }
    const rank = {};
    const place = (i, key) => {
      cols[i].sort((a, b) => key(a) - key(b) || a.name.localeCompare(b.name));
      cols[i].forEach((n, j) => (rank[n.id] = j));
    };
    const mean = (ids, fallback) => {
      const r = (ids || []).map((id) => rank[id]).filter((v) => v != null);
      return r.length ? r.reduce((a, b) => a + b, 0) / r.length : fallback;
    };
    // services: those with traffic first, then by name
    place(1, (n) => ((inn[n.id] || []).length ? 0 : 1000));
    place(0, (n) => mean(out[n.id], 999));
    place(1, (n) => mean(inn[n.id], 500 + (rank[n.id] || 0)));
    place(2, (n) => mean(inn[n.id], 500 + n.name.charCodeAt(0)));
    place(3, (n) => mean(inn[n.id], 999));
    const pos = {};
    let height = 0;
    cols.forEach((list, i) => {
      let y = PAD + HEAD;
      for (const n of list) {
        const h = nodeHeight(n);
        pos[n.id] = { x: PAD + slot[i] * (W + GAP), y, h, n };
        y += h + VGAP;
      }
      height = Math.max(height, y);
    });
    const width = PAD * 2 + shown.length * W + Math.max(0, shown.length - 1) * GAP;
    return { pos, width, height: height + PAD, cols, shown, slot };
  }

  function edgeLabel(from, to) {
    if (from.col === 0) return "routes";
    if (from.col === 1) return "selects";
    if (to.kind === "PersistentVolumeClaim") return "mounts";
    return "reads";
  }

  // render(container, graph, { onOpen(node), contextHue }) -> { focus(id) }
  let seq = 0;
  function render(box, g, opts = {}) {
    const { pos, width, height, cols, shown, slot } = layout(g);
    const arrowId = "rm-arrow-" + ++seq;
    const svg = sv("svg", { viewBox: `0 0 ${width} ${Math.max(height, 240)}`, width, height: Math.max(height, 240),
      class: "rmap", role: "img", "aria-label": `Resource map of namespace ${g.namespace}: ${g.nodes.length} resources, ${g.edges.length} links` });
    svg.append(sv("defs", {},
      sv("marker", { id: arrowId, viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, orient: "auto-start-reverse" },
        sv("path", { d: "M0,0 L10,5 L0,10 z", class: "rm-arrowhead" }))));

    // column headers and lanes
    COLS.forEach((c, i) => {
      if (!shown.includes(i)) return;
      const x = PAD + slot[i] * (W + GAP);
      svg.append(sv("rect", { x: x - 10, y: PAD - 6, width: W + 20, height: Math.max(height, 240) - PAD - 4, rx: 14, class: "rm-lane" }));
      svg.append(txt(x, PAD + 14, c.title, "rm-col"), txt(x, PAD + 32, `${cols[i].length} · ${c.hint}`, "rm-colhint"));
    });

    // edges first, so nodes sit on top
    const edgeEls = [];
    const eg = sv("g", { class: "rm-edges" });
    for (const e of g.edges) {
      const a = pos[e.from], b = pos[e.to];
      if (!a || !b) continue;
      const x1 = a.x + W, y1 = a.y + 22, x2 = b.x - 2, y2 = b.y + 22;
      const mx = (x1 + x2) / 2;
      const path = sv("path", { d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
        class: "rm-edge" + (b.n.missing ? (b.n.health === "bad" ? " missing" : " absent") : ""),
        "marker-end": `url(#${arrowId})` });
      const label = txt(mx, (y1 + y2) / 2 - 4, edgeLabel(a.n, b.n), "rm-elabel", { "text-anchor": "middle" });
      const grp = sv("g", { "data-from": e.from, "data-to": e.to }, path, label);
      eg.append(grp);
      edgeEls.push(grp);
    }
    svg.append(eg);

    // nodes
    const nodeEls = {};
    const ng = sv("g", { class: "rm-nodes" });
    for (const id of Object.keys(pos)) {
      const { x, y, h, n } = pos[id];
      const health = n.missing ? (n.health === "bad" ? "missing" : "absent") : n.health || "none";
      const gEl = sv("g", { class: `rm-node h-${health}`, transform: `translate(${x},${y})`, tabindex: 0, role: "button",
        "aria-label": `${n.kind} ${n.name}${n.status ? ", " + n.status : ""}` });
      gEl.append(sv("rect", { width: W, height: h, rx: 10, class: "rm-card" }));
      gEl.append(sv("rect", { width: 4, height: h - 12, x: 0, y: 6, rx: 2, class: "rm-stripe" }));
      gEl.append(txt(14, 18, n.kind.toUpperCase(), "rm-kind"));
      if (n.status) gEl.append(txt(W - 12, 18, clip(n.status, 22), "rm-status", { "text-anchor": "end" }));
      let yy = 22;
      for (const line of wrapName(n.name, NAME_MAX)) { yy += 16; gEl.append(txt(14, yy, line, "rm-name")); }
      if (n.info) { yy += 16; gEl.append(txt(14, yy, clip(n.info, 38), "rm-info")); }
      if (n.pods && n.pods.length) {
        yy += 12;
        n.pods.forEach((p, i) => {
          const c = sv("circle", { cx: 18 + (i % 20) * 11, cy: yy + Math.floor(i / 20) * 11, r: 4, class: `rm-pod h-${p.health || "none"}` });
          c.append(sv("title", {}, `${p.name} — ${p.status}`));
          gEl.append(c);
        });
        if (n.podsAll > n.pods.length) gEl.append(txt(W - 12, yy + 4, `+${n.podsAll - n.pods.length}`, "rm-info", { "text-anchor": "end" }));
        yy += Math.floor((n.pods.length - 1) / 20) * 11;
      }
      if (n.badges && n.badges.length) {
        let bx = 14;
        const by = h - 22;
        for (const b of n.badges.slice(0, 3)) {
          const w = Math.min(W - 28, 10 + b.text.length * 6.3);
          if (bx + w > W - 10) break;
          const bg = sv("g", { class: `rm-badge h-${b.health || "none"}` }, sv("rect", { x: bx, y: by, width: w, height: 16, rx: 8 }),
            txt(bx + w / 2, by + 11.5, b.text, "", { "text-anchor": "middle" }));
          if (b.title) bg.append(sv("title", {}, b.title));
          gEl.append(bg);
          bx += w + 6;
        }
      }
      gEl.append(sv("title", {}, `${n.kind} ${n.name}${n.status ? " — " + n.status : ""}${n.info ? "\n" + n.info : ""}${n.missing ? "\n(referenced, but not in the namespace)" : ""}\n${opts.onSelect ? "Click for its own topology · double-click to open" : "Double-click to open"}`));
      ng.append(gEl);
      nodeEls[id] = gEl;
    }
    svg.append(ng);

    // The chain through a node: everything reachable upstream and downstream.
    const out = {}, inn = {};
    for (const e of g.edges) { (out[e.from] = out[e.from] || []).push(e.to); (inn[e.to] = inn[e.to] || []).push(e.from); }
    const chain = (id) => {
      const seen = new Set([id]);
      const walk = (start, next) => { const st = [start]; while (st.length) { const v = st.pop(); for (const w of next[v] || []) if (!seen.has(w)) { seen.add(w); st.push(w); } } };
      walk(id, out); walk(id, inn);
      return seen;
    };
    let pinned = null;
    const focus = (id) => {
      const on = id ? chain(id) : null;
      svg.classList.toggle("focusing", !!on);
      for (const [nid, el] of Object.entries(nodeEls)) el.classList.toggle("lit", !!on && on.has(nid));
      for (const el of edgeEls) el.classList.toggle("lit", !!on && on.has(el.dataset.from) && on.has(el.dataset.to));
    };
    for (const [id, el] of Object.entries(nodeEls)) {
      // No dimming on hover: the map stays as it is; a click opens the resource's own view.
      if (!opts.onSelect) {
        el.addEventListener("mouseenter", () => { if (!pinned) focus(id); });
        el.addEventListener("mouseleave", () => { if (!pinned) focus(null); });
      }
      el.addEventListener("click", (e) => { e.stopPropagation(); if (opts.onSelect) return opts.onSelect(pos[id].n); pinned = pinned === id ? null : id; focus(pinned); });
      el.addEventListener("dblclick", () => opts.onOpen && opts.onOpen(pos[id].n));
      el.addEventListener("keydown", (e) => { if (e.key === "Enter") opts.onOpen && opts.onOpen(pos[id].n); if (e.key === " ") { e.preventDefault(); if (opts.onSelect) return opts.onSelect(pos[id].n); pinned = pinned === id ? null : id; focus(pinned); } });
    }
    svg.addEventListener("click", () => { pinned = null; focus(null); });
    box.replaceChildren(svg);
    return { focus: (id) => { pinned = id; focus(id); }, svg };
  }

  // ---------------------------------------------------------------- one chain, top to bottom
  // The resource that was clicked and everything connected to it -- what sends it
  // traffic above, what it uses below -- as rows: Traffic, Services, Workloads, Config.
  // The chosen resource is highlighted; clicking another re-centres on it.
  function renderFlow(box, g, focusId, opts = {}) {
    const out = {}, inn = {};
    for (const e of g.edges) { (out[e.from] = out[e.from] || []).push(e.to); (inn[e.to] = inn[e.to] || []).push(e.from); }
    const seen = new Set([focusId]);
    const walk = (start, next) => { const st = [start]; while (st.length) { const v = st.pop(); for (const w of next[v] || []) if (!seen.has(w)) { seen.add(w); st.push(w); } } };
    walk(focusId, out); walk(focusId, inn);
    const nodes = g.nodes.filter((n) => seen.has(n.id));
    const edges = g.edges.filter((e) => seen.has(e.from) && seen.has(e.to));
    const rows = [[], [], [], []];
    for (const n of nodes) rows[n.col].push(n);
    rows.forEach((r) => r.sort((a, b) => (a.id === focusId ? -1 : b.id === focusId ? 1 : 0) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)));
    const shownRows = [0, 1, 2, 3].filter((i) => rows[i].length);
    const FW = 250, FG = 18, RG = 64, LABEL = 26;
    const avail = Math.max(600, (box.clientWidth || 1100) - 4);
    const perRow = Math.max(1, Math.floor((avail - PAD * 2 + FG) / (FW + FG)));
    // A row with more resources than fit wraps onto several lines.
    const pos = {};
    let y = PAD;
    const rowTops = {};
    let width = 0;
    for (const i of shownRows) {
      rowTops[i] = y;
      y += LABEL;
      const list = rows[i];
      for (let start = 0; start < list.length; start += perRow) {
        const line = list.slice(start, start + perRow);
        const lineW = line.length * FW + (line.length - 1) * FG;
        width = Math.max(width, lineW);
        const h = Math.max(...line.map(nodeHeight));
        line.forEach((n, j) => { pos[n.id] = { x: 0, y, h: nodeHeight(n), n, j, lineW, line: line.length }; });
        y += h + (start + perRow < list.length ? 34 : FG); // room for arrows to reach the next line
      }
      y += RG - FG;
    }
    width = Math.max(width + 2 * 170 + PAD * 2, avail - 24); // left gutter for row titles; fill the canvas without scrolling
    // centre each line
    for (const p of Object.values(pos)) p.x = (width - p.lineW) / 2 + p.j * (FW + FG);
    const height = y - RG + PAD + 10;
    const arrowId = "rm-arrow-" + ++seq;
    const svg = sv("svg", { viewBox: `0 0 ${width} ${height}`, width, height, class: "rmap rflow", role: "img",
      "aria-label": `${nodes.length} resources connected to the selected one, top to bottom` });
    svg.append(sv("defs", {}, sv("marker", { id: arrowId, viewBox: "0 0 10 10", refX: 5, refY: 9, markerWidth: 7, markerHeight: 7, orient: "auto" },
      sv("path", { d: "M0,0 L10,0 L5,10 z", class: "rm-arrowhead" }))));
    for (const i of shownRows) {
      svg.append(txt(PAD, rowTops[i] + 14, COLS[i].title, "rm-col"), txt(PAD, rowTops[i] + 31, `${rows[i].length} · ${COLS[i].hint}`, "rm-colhint"));
      svg.append(sv("line", { x1: PAD + 170, x2: width - PAD, y1: rowTops[i] + 10, y2: rowTops[i] + 10, class: "rflow-rule" }));
    }
    const eg = sv("g", { class: "rm-edges" });
    for (const e of edges) {
      const a = pos[e.from], b = pos[e.to];
      if (!a || !b) continue;
      const x1 = a.x + FW / 2, y1 = a.y + a.h, x2 = b.x + FW / 2, y2 = b.y - 3;
      const my = Math.min((y1 + y2) / 2, y1 + 40);
      const lit = e.from === focusId || e.to === focusId;
      const path = sv("path", { d: `M${x1},${y1} C${x1},${my} ${x2},${my} ${x2},${y2}`,
        class: "rm-edge" + (b.n.missing ? (b.n.health === "bad" ? " missing" : " absent") : "") + (lit ? " flow-lit" : ""), "marker-end": `url(#${arrowId})` });
      const label = txt((x1 + x2) / 2 + 6, my, edgeLabel(a.n, b.n), "rm-elabel flow-label");
      eg.append(sv("g", {}, path, label));
    }
    svg.append(eg);
    const ng = sv("g", { class: "rm-nodes" });
    for (const id of Object.keys(pos)) {
      const { x, y, h, n } = pos[id];
      const health = n.missing ? (n.health === "bad" ? "missing" : "absent") : n.health || "none";
      const gEl = sv("g", { class: `rm-node h-${health}` + (id === focusId ? " focus" : ""), transform: `translate(${x},${y})`, tabindex: 0, role: "button",
        "aria-label": `${n.kind} ${n.name}${n.status ? ", " + n.status : ""}` });
      gEl.append(sv("rect", { width: FW, height: h, rx: 10, class: "rm-card" }), sv("rect", { width: 4, height: h - 12, x: 0, y: 6, rx: 2, class: "rm-stripe" }));
      gEl.append(txt(14, 18, n.kind.toUpperCase(), "rm-kind"));
      if (n.status) gEl.append(txt(FW - 12, 18, clip(n.status, 22), "rm-status", { "text-anchor": "end" }));
      let yy = 22;
      for (const line of wrapName(n.name, NAME_MAX)) { yy += 16; gEl.append(txt(14, yy, line, "rm-name")); }
      if (n.info) { yy += 16; gEl.append(txt(14, yy, clip(n.info, 38), "rm-info")); }
      if (n.pods && n.pods.length) {
        yy += 12;
        n.pods.forEach((p, k) => {
          const c = sv("circle", { cx: 18 + (k % 20) * 11, cy: yy + Math.floor(k / 20) * 11, r: 4, class: `rm-pod h-${p.health || "none"}` });
          c.append(sv("title", {}, `${p.name} — ${p.status}`));
          gEl.append(c);
        });
        yy += Math.floor((n.pods.length - 1) / 20) * 11;
      }
      if (n.badges && n.badges.length) {
        let bx = 14;
        for (const b of n.badges.slice(0, 3)) {
          const w = Math.min(FW - 28, 10 + b.text.length * 6.3);
          if (bx + w > FW - 10) break;
          gEl.append(sv("g", { class: `rm-badge h-${b.health || "none"}` }, sv("rect", { x: bx, y: h - 22, width: w, height: 16, rx: 8 }),
            txt(bx + w / 2, h - 10.5, b.text, "", { "text-anchor": "middle" })));
          bx += w + 6;
        }
      }
      gEl.append(sv("title", {}, `${n.kind} ${n.name}${n.status ? " — " + n.status : ""}\n${id === focusId ? "Double-click to open" : "Click to centre on it · double-click to open"}`));
      gEl.addEventListener("click", () => { if (id !== focusId && opts.onSelect) opts.onSelect(n); });
      gEl.addEventListener("dblclick", () => opts.onOpen && opts.onOpen(n));
      gEl.addEventListener("keydown", (e) => { if (e.key === "Enter") opts.onOpen && opts.onOpen(n); });
      ng.append(gEl);
    }
    svg.append(ng);
    box.replaceChildren(svg);
    return { svg };
  }

  // ---------------------------------------------------------------- cluster map
  // The whole cluster: node pools on the left (each with its nodes as squares,
  // coloured by how much CPU their pods request), namespaces on the right (pods,
  // workloads, health), and a band from each namespace to the pools its pods run on,
  // as thick as the number of pods. Click a namespace to open its map.
  const PW = 330, NW = 300, CGAP = 220;
  const fmtCPU = (m) => (m >= 1000 ? `${(m / 1000).toFixed(m >= 10000 ? 0 : 1)}` : `${(m / 1000).toFixed(2)}`);
  function poolHeight(p) { return 74 + Math.ceil(p.nodes.length / 24) * 13; }
  function nsHeight() { return 54; }

  function renderCluster(box, cm, opts = {}) {
    const showSystem = !!opts.showSystem;
    const nss = cm.namespaces.filter((n) => showSystem || !n.system);
    const nsSet = new Set(nss.map((n) => n.name));
    const links = cm.links.filter((l) => nsSet.has(l.namespace));
    const poolsUsed = new Set(links.map((l) => l.pool));
    const pools = cm.pools.filter((p) => showSystem || poolsUsed.has(p.name) || !p.pods);
    // Namespaces ordered by the pools they mostly run on, so bands cross less.
    const pRank = {}; pools.forEach((p, i) => (pRank[p.name] = i));
    const main = {};
    for (const l of links) if (!main[l.namespace] || l.pods > main[l.namespace].pods) main[l.namespace] = l;
    nss.sort((a, b) => (pRank[main[a.name]?.pool] ?? 99) - (pRank[main[b.name]?.pool] ?? 99) || b.pods - a.pods || a.name.localeCompare(b.name));

    const x0 = PAD, x1 = PAD + PW + CGAP;
    const py = {}, ny = {};
    let y = PAD + HEAD;
    for (const p of pools) { py[p.name] = { y, h: poolHeight(p) }; y += poolHeight(p) + 16; }
    const hp = y;
    y = PAD + HEAD;
    for (const n of nss) { ny[n.name] = { y, h: nsHeight(n) }; y += nsHeight(n) + 10; }
    const H = Math.max(hp, y) + PAD, Wd = x1 + NW + PAD;
    const svg = sv("svg", { viewBox: `0 0 ${Wd} ${H}`, width: Wd, height: H, class: "rmap cmap", role: "img",
      "aria-label": `Cluster map: ${cm.nodes} nodes in ${pools.length} pools, ${cm.pods} pods in ${nss.length} namespaces` });

    svg.append(txt(x0, PAD + 14, "Node pools", "rm-col"), txt(x0, PAD + 32, `${pools.length} pools · ${cm.nodes} nodes · squares are nodes, by CPU requested`, "rm-colhint"));
    svg.append(txt(x1, PAD + 14, "Namespaces", "rm-col"), txt(x1, PAD + 32, `${nss.length}${showSystem ? "" : " app"} namespaces · ${cm.pods} pods · click for its topology`, "rm-colhint"));

    // bands: from the pool's right edge to the namespace's left edge, stacked so each
    // pool's outgoing bands and each namespace's incoming bands don't overlap.
    const maxPods = Math.max(1, ...links.map((l) => l.pods));
    const width = (n) => Math.max(1.5, Math.min(16, 1.5 + 14.5 * Math.sqrt(n / maxPods)));
    const pOff = {}, nOff = {};
    // Squeeze a card's bands to fit its height when many share it.
    const pSum = {}, nSum = {};
    for (const l of links) { pSum[l.pool] = (pSum[l.pool] || 0) + width(l.pods) + 1; nSum[l.namespace] = (nSum[l.namespace] || 0) + width(l.pods) + 1; }
    const pScale = (p) => Math.min(1, ((py[p]?.h || 60) - 28) / (pSum[p] || 1));
    const nScale = (n) => Math.min(1, ((ny[n]?.h || 54) - 18) / (nSum[n] || 1));
    const bands = sv("g", { class: "rm-edges" });
    const bandEls = [];
    links.slice().sort((a, b) => (pRank[a.pool] ?? 99) - (pRank[b.pool] ?? 99) || (ny[a.namespace]?.y || 0) - (ny[b.namespace]?.y || 0)).forEach((l) => {
      const P = py[l.pool], N = ny[l.namespace];
      if (!P || !N) return;
      const w = Math.max(1, width(l.pods) * Math.min(pScale(l.pool), nScale(l.namespace)));
      const wa = (width(l.pods) + 1) * pScale(l.pool), wb = (width(l.pods) + 1) * nScale(l.namespace);
      const ya = P.y + 14 + (pOff[l.pool] = (pOff[l.pool] || 0) + wa) - wa / 2;
      const yb = N.y + 9 + (nOff[l.namespace] = (nOff[l.namespace] || 0) + wb) - wb / 2;
      const xa = x0 + PW, xb = x1, mx = (xa + xb) / 2;
      const path = sv("path", { d: `M${xa},${ya} C${mx},${ya} ${mx},${yb} ${xb},${yb}`, class: "rm-band", "stroke-width": w });
      const lbl = txt(mx, (ya + yb) / 2 - w / 2 - 3, `${l.pods} pod${l.pods === 1 ? "" : "s"}`, "rm-elabel", { "text-anchor": "middle" });
      const g = sv("g", { "data-from": "pool:" + l.pool, "data-to": "ns:" + l.namespace }, path, lbl, sv("title", {}, `${l.namespace} runs ${l.pods} pod${l.pods === 1 ? "" : "s"} on ${l.pool}`));
      bands.append(g); bandEls.push(g);
    });
    svg.append(bands);

    const nodeEls = {};
    const nodesG = sv("g", { class: "rm-nodes" });
    for (const p of pools) {
      const { y, h } = py[p.name];
      const cpu = p.cpuAlloc ? Math.round((p.cpuReq * 100) / p.cpuAlloc) : 0;
      const mem = p.memAlloc ? Math.round((p.memReq * 100) / p.memAlloc) : 0;
      const g = sv("g", { class: `rm-node h-${p.health || "none"}`, transform: `translate(${x0},${y})`, tabindex: 0, role: "button",
        "aria-label": `Node pool ${p.name}, ${p.nodes.length} nodes, CPU ${cpu}% requested` });
      g.append(sv("rect", { width: PW, height: h, rx: 10, class: "rm-card" }), sv("rect", { width: 4, height: h - 12, x: 0, y: 6, rx: 2, class: "rm-stripe" }));
      g.append(txt(14, 18, p.source === "karpenter" ? "KARPENTER NODEPOOL" : p.source === "eks" ? "NODE GROUP" : "NODES", "rm-kind"));
      g.append(txt(PW - 12, 18, `${p.nodes.length} node${p.nodes.length === 1 ? "" : "s"} · ${p.pods} pods`, "rm-status", { "text-anchor": "end" }));
      g.append(txt(14, 38, clip(p.name, 34), "rm-name"));
      // capacity bars
      const bar = (yy, label, pct, used, total) => {
        g.append(txt(14, yy + 8, label, "rm-info"));
        g.append(sv("rect", { x: 56, y: yy, width: PW - 160, height: 9, rx: 4.5, class: "rm-bar-bg" }));
        g.append(sv("rect", { x: 56, y: yy, width: Math.max(2, ((PW - 160) * Math.min(pct, 100)) / 100), height: 9, rx: 4.5,
          class: "rm-bar " + (pct >= 90 ? "hot" : pct >= 70 ? "warm" : "") }));
        g.append(txt(PW - 12, yy + 8, `${pct}% · ${used}/${total}`, "rm-info", { "text-anchor": "end" }));
      };
      bar(46, "CPU", cpu, fmtCPU(p.cpuReq), fmtCPU(p.cpuAlloc));
      bar(59, "Memory", mem, `${Math.round(p.memReq / 2 ** 30)}`, `${Math.round(p.memAlloc / 2 ** 30)} GiB`);
      p.nodes.forEach((n, i) => {
        const r = sv("rect", { x: 14 + (i % 24) * 12.5, y: 74 + Math.floor(i / 24) * 13, width: 10, height: 10, rx: 2,
          class: `rm-nodesq ${n.health !== "ok" ? "h-" + n.health : n.cpuPct >= 90 ? "hot" : n.cpuPct >= 60 ? "warm" : "cool"}${n.spot ? " spot" : ""}` });
        r.append(sv("title", {}, `${n.name}\n${n.type} · ${n.zone}${n.spot ? " · spot" : ""}\n${n.status} · ${n.pods} pods · CPU ${n.cpuPct}% requested`));
        g.append(r);
      });
      g.append(sv("title", {}, `${p.name}\n${p.types.join(", ")}`));
      nodesG.append(g); nodeEls["pool:" + p.name] = g;
      g.addEventListener("click", (e) => { e.stopPropagation(); pin("pool:" + p.name); });
      g.addEventListener("dblclick", () => opts.onPool && opts.onPool(p));
    }
    for (const n of nss) {
      const { y, h } = ny[n.name];
      const g = sv("g", { class: `rm-node h-${n.health || "none"}`, transform: `translate(${x1},${y})`, tabindex: 0, role: "button",
        "aria-label": `Namespace ${n.name}, ${n.pods} pods${n.failing ? ", " + n.failing + " failing" : ""}` });
      g.append(sv("rect", { width: NW, height: h, rx: 10, class: "rm-card" }), sv("rect", { width: 4, height: h - 12, x: 0, y: 6, rx: 2, class: "rm-stripe" }));
      g.append(txt(14, 20, clip(n.name, n.system ? 22 : 28), "rm-name"));
      const st = n.failing ? `${n.failing} failing` : n.warning ? `${n.warning} not ready` : n.pods ? "healthy" : "no pods";
      g.append(txt(NW - 12, 20, (n.system ? "platform · " : "") + st, "rm-status", { "text-anchor": "end" }));
      g.append(txt(14, 40, clip(`${n.workloads} workload${n.workloads === 1 ? "" : "s"} · ${n.pods} pods · ${n.services} svc${n.ingresses ? " · " + n.ingresses + " ing" : ""} · ${fmtCPU(n.cpuReq)} CPU`, 41), "rm-info"));
      nodesG.append(g); nodeEls["ns:" + n.name] = g;
      g.addEventListener("click", (e) => { e.stopPropagation(); opts.onNamespace && opts.onNamespace(n.name); });
      g.addEventListener("keydown", (e) => { if (e.key === "Enter") opts.onNamespace && opts.onNamespace(n.name); });
    }
    svg.append(nodesG);

    let pinned = null;
    const focus = (id) => {
      svg.classList.toggle("focusing", !!id);
      const on = new Set(id ? [id] : []);
      if (id) for (const b of bandEls) if (b.dataset.from === id || b.dataset.to === id) { on.add(b.dataset.from); on.add(b.dataset.to); }
      for (const [k, el] of Object.entries(nodeEls)) el.classList.toggle("lit", on.has(k));
      for (const b of bandEls) b.classList.toggle("lit", !!id && (b.dataset.from === id || b.dataset.to === id));
    };
    const pin = (id) => { pinned = pinned === id ? null : id; focus(pinned); };
    for (const [k, el] of Object.entries(nodeEls)) {
      el.addEventListener("mouseenter", () => { if (!pinned) focus(k); });
      el.addEventListener("mouseleave", () => { if (!pinned) focus(null); });
    }
    svg.addEventListener("click", () => { pinned = null; focus(null); });
    box.replaceChildren(svg);
    return { svg };
  }

  // ---------------------------------------------------------------- helm topology
  // One release: its card on the left, every resource its manifest created on the
  // right, grouped by kind (in reading order: traffic, workloads, config, RBAC...),
  // each with its live state and a band from the release. Missing ones are dashed red.
  function renderHelm(box, r, opts = {}) {
    // Fit the canvas: kind groups flow into as many columns as the width allows.
    const avail = Math.max(700, (box.clientWidth || 1100) - 4);
    const RW = 236, GX = 70, ROWH = 28, GROUPGAP = 22;
    const x0 = PAD + RW + GX;
    const OW = 220, CG = 16;
    const cols = Math.max(1, Math.min(4, Math.floor((avail - x0 - PAD + CG) / (OW + CG))));
    const groups = [];
    for (const o of r.objects) {
      let g = groups[groups.length - 1];
      if (!g || g.kind !== o.kind) groups.push((g = { kind: o.kind, items: [] }));
      g.items.push(o);
    }
    const colX = (c) => x0 + c * (OW + CG);
    const colH = Array(cols).fill(PAD + HEAD);
    const place = [];
    for (const g of groups) { // each kind group into the shortest column, in reading order
      let c = 0;
      for (let i = 1; i < cols; i++) if (colH[i] < colH[c] - 1) c = i;
      place.push({ g, c, y: colH[c] });
      colH[c] += 18 + g.items.length * ROWH + GROUPGAP;
    }
    const usedCols = Math.max(1, Math.min(cols, groups.length));
    const H = Math.max(...colH, PAD + HEAD + 180) + PAD;
    const W = colX(usedCols - 1) + OW + PAD;
    const svg = sv("svg", { viewBox: `0 0 ${W} ${H}`, width: W, height: H, class: "rmap hmap", role: "img",
      "aria-label": `Helm release ${r.namespace}/${r.name}: ${r.objects.length} resources, ${r.counts.Missing} missing, ${r.counts.Failing} failing` });
    svg.append(txt(PAD, PAD + 14, "Release", "rm-col"), txt(PAD, PAD + 32, "click for values, history, manifest", "rm-colhint"));
    svg.append(txt(x0, PAD + 14, "Resources it created", "rm-col"),
      txt(x0, PAD + 32, `${r.objects.length} in the manifest · ${r.counts.Missing} missing · ${r.counts.Failing} failing · ${r.counts.Warning} warning`, "rm-colhint"));

    const rh = 128, ry = PAD + HEAD;
    const rel = sv("g", { class: `rm-node h-${r.health || "none"}`, transform: `translate(${PAD},${ry})`, tabindex: 0, role: "button", "aria-label": `Release ${r.name}` });
    rel.append(sv("rect", { width: RW, height: rh, rx: 10, class: "rm-card" }), sv("rect", { width: 4, height: rh - 12, x: 0, y: 6, rx: 2, class: "rm-stripe" }));
    rel.append(txt(14, 20, clip("HELM RELEASE · " + r.namespace, 28), "rm-kind"), txt(RW - 12, 20, r.status, "rm-status", { "text-anchor": "end" }));
    rel.append(txt(14, 42, clip(r.name, 24), "rm-name"));
    rel.append(txt(14, 62, clip(`${r.chart}-${r.chartVersion}`, 30), "rm-info"));
    rel.append(txt(14, 80, clip(`revision ${r.revision}${r.appVersion ? " · app " + r.appVersion : ""}`, 30), "rm-info"));
    const tally = [[r.counts.Total - r.counts.Missing - r.counts.Failing - r.counts.Warning, "ok"], [r.counts.Warning, "warn"], [r.counts.Failing + r.counts.Missing, "bad"]];
    let tx = 14;
    for (const [n, h] of tally) {
      if (!n) continue;
      rel.append(sv("circle", { cx: tx + 4, cy: 104, r: 4, class: "rm-pod h-" + h }), txt(tx + 12, 108, String(n), "rm-info"));
      tx += 24 + String(n).length * 7;
    }
    rel.addEventListener("click", () => opts.onRelease && opts.onRelease());
    rel.addEventListener("keydown", (e) => { if (e.key === "Enter") opts.onRelease && opts.onRelease(); });

    // One band per kind group (not per object): from the release to the group's label.
    // Hovering an object lights its group's band.
    const edges = sv("g", { class: "rm-edges" });
    const nodes = sv("g", { class: "rm-nodes" });
    const q = (opts.highlight || "").toLowerCase();
    for (const { g, c, y } of place) {
      const x = colX(c);
      const bad = g.items.some((o) => o.missing || o.health === "bad");
      const warn = g.items.some((o) => o.health === "warn");
      const label = txt(x, y + 11, `${g.kind}${g.items.length > 1 ? "  ×" + g.items.length : ""}`, "rm-kind hm-group");
      nodes.append(label);
      const xa = PAD + RW, ya = ry + rh / 2, xb = x - 6, yb = y + 18 + (g.items.length * ROWH) / 2 - 3;
      const mx = xa + (xb - xa) * 0.5;
      const eg = sv("g", { class: bad ? "bad" : warn ? "warn" : "" },
        sv("path", { d: `M${xa},${ya} C${mx},${ya} ${mx},${yb} ${xb},${yb}`, class: "rm-edge hm-edge" + (bad ? " missing" : ""),
          "stroke-width": Math.min(6, 1 + Math.sqrt(g.items.length)) }));
      edges.append(eg);
      // bracket on the left of the group
      nodes.append(sv("rect", { x: x - 6, y: y + 18, width: 2, height: g.items.length * ROWH - 6, rx: 1, class: "hm-bracket" + (bad ? " bad" : warn ? " warn" : "") }));
      g.items.forEach((o, i) => {
        const oy = y + 18 + i * ROWH;
        const health = o.missing ? "missing" : o.health || "none";
        const n = sv("g", { class: `rm-node hm-obj h-${health}` + (q && (o.kind + " " + o.name).toLowerCase().includes(q) ? " lit-q" : ""),
          transform: `translate(${x},${oy})`, tabindex: 0, role: "button",
          "aria-label": `${o.kind} ${o.namespace ? o.namespace + "/" : ""}${o.name}, ${o.status}` });
        n.append(sv("rect", { width: OW, height: ROWH - 5, rx: 6, class: "rm-card" }), sv("rect", { width: 3, height: ROWH - 13, x: 0, y: 4, rx: 1.5, class: "rm-stripe" }));
        const showStatus = o.status && o.status !== "Exists";
        n.append(txt(10, 16, clip(o.name, showStatus ? 19 : 27), "rm-name hm-objname"));
        if (showStatus) n.append(txt(OW - 8, 16, clip(o.status, 14), "rm-status", { "text-anchor": "end" }));
        n.append(sv("title", {}, `${o.kind} ${o.namespace ? o.namespace + "/" : ""}${o.name}\n${o.status}${o.missing ? "\nIn the release's manifest, but not in the cluster." : ""}\nDouble-click to open`));
        n.addEventListener("dblclick", () => opts.onOpen && opts.onOpen(o));
        n.addEventListener("keydown", (e) => { if (e.key === "Enter") opts.onOpen && opts.onOpen(o); });
        nodes.append(n);
      });
    }
    svg.append(edges, nodes, rel);
    box.replaceChildren(svg);
    return { svg };
  }

  root.KCGraph = { render, renderCluster, renderHelm, renderFlow };
})(window);
