// The dock: a tabbed panel along the bottom for long-running sessions -- shells into
// pods or onto nodes, and logs from one or many pods. Each tab keeps its own
// connection, so switching cluster, resource or tab never drops a session, and tabs
// from different clusters sit side by side with the cluster's colour on each.
"use strict";

const Dock = (() => {
  const tabs = [];
  let active = null;
  let seq = 0;
  // Layout: "tabs" (one session at a time, the default) or "split" (every session
  // side by side, each with its own title bar). Remembered.
  let layout = (() => { try { return localStorage.getItem("kc.dockLayout") === "split" ? "split" : "tabs"; } catch (_) { return "tabs"; } })();
  let splitDir = (() => { try { return localStorage.getItem("kc.dockSplitDir") === "rows" ? "rows" : "cols"; } catch (_) { return "cols"; } })();

  const root = () => document.querySelector("#dock");
  const strip = () => document.querySelector("#dockTabs");
  const body = () => document.querySelector("#dockBody");

  function show() {
    const d = root();
    d.hidden = tabs.length === 0;
    const split = layout === "split" && tabs.length > 1;
    d.classList.toggle("split", split);
    body().classList.toggle("split-rows", split && splitDir === "rows");
    const sb = document.querySelector("#dockSplit");
    if (sb) {
      sb.hidden = tabs.length < 2;
      sb.setAttribute("aria-pressed", String(layout === "split"));
      sb.title = layout === "split" ? "Back to tabs" : "Show every session side by side";
      sb.textContent = layout === "split" ? "▭ Tabs" : "◫ Split";
    }
    const db = document.querySelector("#dockDir");
    if (db) {
      db.hidden = !split;
      db.textContent = splitDir === "rows" ? "⇅" : "⇆";
      db.title = splitDir === "rows" ? "Stacked; click for side by side" : "Side by side; click to stack";
    }
    document.querySelector("#app").classList.toggle("with-dock", tabs.length > 0);
    const btn = document.querySelector("#dockToggle");
    if (btn) btn.textContent = d.classList.contains("min") ? "▴" : "▾";
  }

  function render() {
    strip().replaceChildren(...tabs.map((t) => el("div", {
        class: "dock-tab" + (t === active ? " on" : "") + (t.dead ? " dead" : ""), role: "tab",
        "aria-selected": String(t === active), title: t.title, style: `--h:${t.hue}`,
        onclick: () => select(t),
        onauxclick: (e) => { if (e.button === 1) close(t); },
      },
      el("span", { class: "dock-ico", text: t.kind === "logs" ? "≡" : t.kind === "node" ? "⌂" : "›_" }),
      el("span", { class: "ctx-dot" + (t.protected ? " prot" : "") }),
      el("span", { class: "dock-label", text: t.label }),
      el("button", { type: "button", class: "dock-x", title: "Close", "aria-label": "Close tab",
        onclick: (e) => { e.stopPropagation(); close(t); } }, "✕"))));
    show();
  }

  function select(t) {
    active = t;
    const split = layout === "split" && tabs.length > 1;
    for (const x of tabs) {
      x.pane.hidden = split ? false : x !== t;
      x.pane.classList.toggle("on", x === t);
    }
    if (root().classList.contains("min")) root().classList.remove("min");
    render();
    if (split) { for (const x of tabs) if (x.onShow && x !== t) setTimeout(x.onShow, 0); }
    if (t.onShow) setTimeout(t.onShow, 0);
  }

  function setLayout(l) {
    layout = l;
    try { localStorage.setItem("kc.dockLayout", l); } catch (_) {}
    if (active) select(active); else render();
  }
  function setDir(d) {
    splitDir = d;
    try { localStorage.setItem("kc.dockSplitDir", d); } catch (_) {}
    if (active) select(active); else render();
  }

  function close(t) {
    const i = tabs.indexOf(t);
    if (i < 0) return;
    try { t.dispose(); } catch (_) {}
    t.pane.remove();
    tabs.splice(i, 1);
    if (active === t) active = tabs[Math.min(i, tabs.length - 1)] || null;
    if (active) select(active); else render();
  }

  function add(t) {
    t.id = ++seq;
    t.hue = ctxHue(t.context);
    t.protected = isProtected(t.context);
    // Each pane has its own small title bar, shown only in split view: which session
    // it is, and a close button. Clicking a pane makes it the active one.
    t.head = el("div", { class: "dock-pane-head", style: `--h:${t.hue}` },
      el("span", { class: "dock-ico", text: t.kind === "logs" ? "≡" : t.kind === "node" ? "⌂" : "›_" }),
      el("span", { class: "dock-label", text: t.label, title: t.title }),
      el("button", { type: "button", class: "dock-x", title: "Close", "aria-label": "Close", onclick: (e) => { e.stopPropagation(); close(t); } }, "✕"));
    t.inner = el("div", { class: "dock-pane-body" });
    t.pane = el("div", { class: "dock-pane", onmousedown: () => { if (active !== t) { active = t; render(); for (const x of tabs) x.pane.classList.toggle("on", x === t); } } }, t.head, t.inner);
    body().append(t.pane);
    tabs.push(t);
    t.mount(t.inner);
    select(t);
    return t;
  }

  // ------------------------------------------------------------------ shells
  function shell(row, isNode) {
    const label = `${short(row.context)} · ${row.name}`;
    let sock = null, term = null, ro = null;
    return add({
      kind: isNode ? "node" : "pod", context: row.context, label, title: `${isNode ? "Node shell" : "Shell"} — ${row.context} / ${row.namespace ? row.namespace + "/" : ""}${row.name}`,
      async mount(pane) {
        const status = el("div", { class: "term-status", text: isNode ? `Starting a shell on ${row.name}… a helper pod is being scheduled.` : `Connecting to ${row.name}…` });
        const box = el("div", { class: "term" });
        pane.append(el("div", { class: "term-wrap" }, status, box));
        let container = "";
        if (!isNode) {
          try {
            const cs = await api("/api/containers?" + qs({ context: row.context, namespace: row.namespace, name: row.name }));
            container = cs[cs.length - 1];
          } catch (e) { status.textContent = e.message; return; }
        }
        term = new Terminal({ cursorBlink: true, fontSize: 13, fontFamily: "ui-monospace, 'SF Mono', Menlo, monospace", theme: termTheme() });
        const fit = new FitAddon.FitAddon();
        term.loadAddon(fit); term.open(box);
        sock = new WebSocket(`ws://${location.host}/ws/exec?` + qs(isNode
          ? { token: TOKEN, context: row.context, node: row.name }
          : { token: TOKEN, context: row.context, namespace: row.namespace, name: row.name, container }));
        sock.binaryType = "arraybuffer";
        const sendSize = () => sock && sock.readyState === 1 && sock.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
        sock.onopen = () => { status.textContent = isNode ? `Root shell on ${row.name} · ${short(row.context)}. The helper pod is removed when you close this tab.` : `Shell in ${row.name} · ${short(row.context)}`; sendSize(); term.focus(); };
        sock.onmessage = (ev) => term && term.write(typeof ev.data === "string" ? ev.data : new Uint8Array(ev.data));
        sock.onclose = () => { status.textContent = "Session closed."; this.dead = true; render(); if (term) term.write("\r\n\x1b[90m[session closed]\x1b[0m\r\n"); };
        const enc = new TextEncoder();
        term.onData((d) => sock && sock.readyState === 1 && sock.send(enc.encode(d)));
        let last = "", t = null;
        const refit = () => { clearTimeout(t); t = setTimeout(() => {
          const key = `${box.clientWidth}x${box.clientHeight}`;
          if (!term || key === last || !box.clientWidth || !box.clientHeight) return;
          last = key; try { fit.fit(); sendSize(); } catch (_) {}
        }, 120); };
        ro = new ResizeObserver(refit); ro.observe(box);
        this.onShow = () => { refit(); term && term.focus(); };
        refit();
      },
      dispose() { if (ro) ro.disconnect(); if (sock) { sock.onclose = null; sock.close(); } if (term) term.dispose(); },
    });
  }

  // ------------------------------------------------------------------ logs
  // Several pods, one view: each line carries the kubelet's timestamp, so lines are
  // merged in time order across pods (and clusters), each pod in its own colour.
  const POD_HUES = [212, 152, 32, 280, 340, 186, 92, 8, 250, 56, 122, 300];
  const MAX_LOG_PODS = 20;

  // RFC3339Nano drops trailing zeros (".28216527Z" is .282165270), so the text does
  // not sort. Keep [whole seconds, nanoseconds] as two numbers: one combined number
  // would need more precision than a double has.
  function tsKey(ts) {
    const m = (ts || "").match(/^(.*?)(?:\.(\d+))?Z$/);
    if (!m) return [0, 0];
    return [Date.parse(m[1] + "Z") || 0, Number((m[2] || "").padEnd(9, "0").slice(0, 9))];
  }

  function logs(rows) {
    rows = rows.slice(0, MAX_LOG_PODS);
    const one = rows.length === 1;
    const ctxs = [...new Set(rows.map((r) => r.context))];
    const label = one ? `${short(rows[0].context)} · ${rows[0].name}` : `Logs · ${rows.length} pods${ctxs.length > 1 ? ` · ${ctxs.length} clusters` : ""}`;
    const socks = [];
    const st = { lines: [], seq: 0, find: "", follow: true, muted: new Set(), previous: false, wrap: true, pending: false };
    const pods = rows.map((r, i) => ({ row: r, key: `${r.context}|${r.namespace}|${r.name}`, hue: POD_HUES[i % POD_HUES.length], n: 0, state: "connecting" }));
    let pre, legend, count, paused;
    return add({
      kind: "logs", context: rows[0].context, label,
      title: rows.map((r) => `${short(r.context)}/${r.namespace}/${r.name}`).join("\n"),
      mount(pane) {
        pre = el("pre", { class: "dock-log" });
        legend = el("div", { class: "log-legend" });
        count = el("span", { class: "muted grow" });
        paused = el("button", { type: "button", class: "ghost small", hidden: true, text: "Jump to newest", onclick: () => { st.follow = true; draw(); } });
        const find = el("input", { placeholder: "Find in logs", style: "width:200px", oninput: () => { st.find = find.value; draw(); } });
        const prev = el("label", {}, el("input", { type: "checkbox", onchange: (e) => { st.previous = e.target.checked; connect(); } }), " Previous (crashed)");
        const copyBtn = el("button", { type: "button", class: "ghost small", text: "Copy", onclick: () => copy(visible().map(plain).join("\n")) });
        // Several pods merged: one click opens each pod in its own tab, side by side.
        const apart = one ? null : el("button", { type: "button", class: "ghost small", text: "◫ One per pod",
          title: "Open each pod's logs in its own pane, side by side",
          onclick: () => { const me = tabs.find((x) => x.pane.contains(pane)); for (const r of rows) logs([r]); setLayout("split"); if (me) close(me); } });
        // One pod: its name, namespace and cluster above the log, so it is always clear
        // whose log this is (tab labels get cut when many are open).
        const head = one ? el("div", { class: "log-head", title: `${rows[0].context}\n${rows[0].namespace}/${rows[0].name}` },
          el("span", { class: "ctx-dot" + (isProtected(rows[0].context) ? " prot" : ""), style: `background:hsl(${ctxHue(rows[0].context)} 60% 52%)` }),
          el("strong", { text: rows[0].name }), el("span", { text: `${rows[0].namespace} · ${short(rows[0].context)}` })) : null;
        pane.append(el("div", { class: "pane logs" }, head, el("div", { class: "tools" }, find, prev, count, paused, apart, copyBtn), legend, pre));
        pre.onscroll = () => { const at = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24; if (at !== st.follow) { st.follow = at; paused.hidden = at; } };
        connect();
      },
      dispose() { for (const s of socks) { s.onclose = null; s.close(); } },
    });

    function connect() {
      for (const s of socks) { s.onclose = null; s.close(); }
      socks.length = 0; st.lines = [];
      for (const p of pods) {
        p.n = 0; p.state = "connecting";
        const s = new WebSocket(`ws://${location.host}/ws/logs?` + qs({ token: TOKEN, context: p.row.context, namespace: p.row.namespace,
          name: p.row.name, previous: st.previous, follow: !st.previous, tail: one ? 1000 : 300, timestamps: true }));
        s.onopen = () => { p.state = "live"; drawLegend(); };
        s.onmessage = (ev) => {
          if (ev.data.startsWith("\x00")) { p.state = ev.data.startsWith("\x00error:") ? "error: " + ev.data.slice(7).trim() : "ended"; drawLegend(); return; }
          for (const raw of ev.data.split("\n")) {
            const sp = raw.indexOf(" ");
            const ts = sp > 0 && /^\d{4}-\d\d-\d\dT/.test(raw) ? raw.slice(0, sp) : "";
            st.lines.push({ ts, at: tsKey(ts), seq: st.seq++, text: ts ? raw.slice(sp + 1) : raw, pod: p });
            p.n++;
          }
          schedule();
        };
        s.onclose = () => { if (p.state === "live") p.state = "ended"; drawLegend(); };
        socks.push(s);
      }
      drawLegend(); draw();
    }

    function schedule() { if (st.pending) return; st.pending = true; requestAnimationFrame(() => { st.pending = false; draw(); drawLegend(); }); }

    function visible() {
      const f = st.find.toLowerCase();
      // By time; lines with the same instant keep the order they arrived in.
      const all = one ? st.lines : st.lines.slice().sort((a, b) => a.at[0] - b.at[0] || a.at[1] - b.at[1] || a.seq - b.seq);
      return all.filter((l) => !st.muted.has(l.pod.key) && (!f || l.text.toLowerCase().includes(f)));
    }
    const plain = (l) => (one ? l.text : `${l.pod.row.name}  ${l.text}`);

    function draw() {
      if (st.lines.length > 30000) st.lines.splice(0, st.lines.length - 30000);
      const lines = visible().slice(-5000);
      const f = st.find.toLowerCase();
      const frag = document.createDocumentFragment();
      for (const l of lines) {
        const r = KCLog.render(l.text, f);
        const row = el("div", { class: "log-line" + (one ? " one" : "") + (r.level ? " lv-" + r.level : "") });
        if (!one) row.append(el("span", { class: "log-src", style: `--h:${l.pod.hue}`, text: l.pod.row.name, title: `${short(l.pod.row.context)} / ${l.pod.row.namespace}` }));
        row.append(el("span", { class: "log-ts", text: l.ts ? l.ts.slice(11, 23) : "", title: l.ts, "data-ts": l.ts }));
        const txt = el("span", { class: "log-text" }, r.node);
        row.append(txt);
        frag.append(row);
      }
      pre.replaceChildren(frag);
      count.textContent = `${lines.length.toLocaleString()} lines${st.find ? ` matching` : ""}`;
      if (st.follow) pre.scrollTop = pre.scrollHeight;
    }

    function drawLegend() {
      if (one) { legend.hidden = true; return; }
      legend.replaceChildren(...pods.map((p) => el("button", {
          type: "button", class: "log-chip" + (st.muted.has(p.key) ? " off" : ""), style: `--h:${p.hue}`,
          title: `${p.row.context}\n${p.row.namespace}/${p.row.name}\n${p.state}\nClick to hide or show its lines`,
          onclick: () => { st.muted.has(p.key) ? st.muted.delete(p.key) : st.muted.add(p.key); drawLegend(); draw(); },
        },
        el("span", { class: "log-sw" }),
        ctxs.length > 1 ? el("span", { class: "muted", text: short(p.row.context) + " ·" }) : null,
        p.row.name,
        el("span", { class: "muted", text: p.state === "live" ? `${p.n}` : p.state.startsWith("error") ? "error" : p.state }))));
    }
  }

  function wire() {
    document.querySelector("#dockToggle").onclick = () => { root().classList.toggle("min"); show(); if (active && active.onShow) setTimeout(active.onShow, 50); };
    document.querySelector("#dockCloseAll").onclick = () => { for (const t of tabs.slice()) close(t); };
    document.querySelector("#dockSplit").onclick = () => setLayout(layout === "split" ? "tabs" : "split");
    document.querySelector("#dockDir").onclick = () => setDir(splitDir === "rows" ? "cols" : "rows");
  }

  return { shell, logs, wire, count: () => tabs.length, MAX_LOG_PODS, setLayout, layout: () => layout };
})();
