// K-Vision desktop UI. Plain JS, no build step: the Go server embeds this file.
// Everything from the cluster is untrusted text and reaches the DOM through
// textContent, never innerHTML.
"use strict";

const $ = (s) => document.querySelector(s);
const el = (tag, props = {}, ...kids) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) e.append(k);
  return e;
};

// ------------------------------------------------------------------ session
// The URL `kv ui` opened carries the token in the fragment, which is never sent
// to a server or written to a log. Keep it for this tab and clear the address bar.
let TOKEN = "";
try {
  const m = location.hash.match(/token=([0-9a-f]+)/);
  if (m) { TOKEN = m[1]; sessionStorage.setItem("kcToken", TOKEN); history.replaceState(null, "", location.pathname); }
  else TOKEN = sessionStorage.getItem("kcToken") || "";
} catch (_) {}

async function api(path, opts = {}) {
  const res = await fetch(path, { ...opts,
    headers: { "X-KC-Token": TOKEN, "Content-Type": "application/json", ...(opts.headers || {}) } });
  const text = await res.text();
  let data;
  try { data = JSON.parse(text); } catch (_) { data = { error: text.trim() }; }
  if (res.status === 401) { sessionExpired(); throw new Error("Session expired"); }
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}
// The server restarted with a different token (or this tab is from before an update).
// Say so once, with the fix, instead of an error on every request.
let expiredShown = false;
function sessionExpired() {
  if (expiredShown) return;
  expiredShown = true;
  try { sessionStorage.removeItem("kcToken"); } catch (_) {}
  const box = el("div", { class: "overlay" }, el("div", { class: "dialog" },
    el("h3", { text: "K-Vision was restarted" }),
    el("p", { text: "This window belongs to the previous run. Close it and open K-Vision again (Spotlight, or kv ui in a terminal)." }),
    el("div", { class: "row" }, el("button", { type: "button", class: "primary", text: "Close window", onclick: () => window.close() }))));
  document.body.append(box);
}
const qs = (o) => new URLSearchParams(Object.entries(o).filter(([, v]) => v !== undefined && v !== null && v !== "")).toString();
const post = (path, body) => api(path, { method: "POST", body: JSON.stringify(body) });
const store = {
  get(k) { try { return localStorage.getItem("kc." + k); } catch (_) { return null; } },
  set(k, v) { try { v == null ? localStorage.removeItem("kc." + k) : localStorage.setItem("kc." + k, v); } catch (_) {} },
};

// ------------------------------------------------------------------ state
const S = {
  contexts: [], ctx: "", ns: "", kinds: [], kind: null, everywhere: false,
  rows: [], cols: [], errors: [], filter: "", health: "all", sort: null, sortDir: 1,
  sel: -1, visible: [], loading: false, timer: null, panel: null, seq: 0, loadedAt: 0,
  onlyCtx: null, // all-clusters view narrowed to one cluster from the summary bar
  errorsOpen: false,
  useDock: true, // shells and logs open as tabs in the bottom dock
  hiddenCtx: [],
  picked: new Map(), // key -> row, the rows ticked for a bulk action
};

// Sidebar sections, in Lens's order. A kind the cluster does not serve is left out.
// Labels are what people call them, not the API kind ("Pod Disruption Budgets").
const NAV = [
  ["Workloads", "▤", [["overview", "Namespace Overview"], ["pods", "Pods"], ["deployments.apps", "Deployments"], ["statefulsets.apps", "StatefulSets"],
    ["daemonsets.apps", "DaemonSets"], ["replicasets.apps", "ReplicaSets"], ["jobs.batch", "Jobs"], ["cronjobs.batch", "CronJobs"]]],
  ["Config", "⚙", [["configmaps", "ConfigMaps"], ["secrets", "Secrets"], ["resourcequotas", "Resource Quotas"],
    ["limitranges", "Limit Ranges"],
    ["poddisruptionbudgets.policy", "Pod Disruption Budgets"], ["priorityclasses.scheduling.k8s.io", "Priority Classes"],
    ["runtimeclasses.node.k8s.io", "Runtime Classes"], ["leases.coordination.k8s.io", "Leases"]]],
  ["Network", "⇄", [["services", "Services"], ["endpoints", "Endpoints"], ["endpointslices.discovery.k8s.io", "Endpoint Slices"],
    ["ingresses.networking.k8s.io", "Ingresses"], ["ingressclasses.networking.k8s.io", "Ingress Classes"],
    ["networkpolicies.networking.k8s.io", "Network Policies"], ["portforwards", "Port Forwarding"]]],
  ["Storage", "▣", [["persistentvolumeclaims", "Persistent Volume Claims"], ["persistentvolumes", "Persistent Volumes"],
    ["storageclasses.storage.k8s.io", "Storage Classes"]]],
  ["Access Control", "⛨", [["serviceaccounts", "Service Accounts"], ["clusterroles.rbac.authorization.k8s.io", "Cluster Roles"],
    ["roles.rbac.authorization.k8s.io", "Roles"], ["clusterrolebindings.rbac.authorization.k8s.io", "Cluster Role Bindings"],
    ["rolebindings.rbac.authorization.k8s.io", "Role Bindings"]]],
  ["Cluster", "◉", [["warnings", "Warnings"], ["nodes", "Nodes"], ["namespaces", "Namespaces"], ["events", "Events"]]],
  ["Argo CD", "⟳", [["argo", "Dashboard"], ["applications.argoproj.io", "Applications"], ["applicationsets.argoproj.io", "Application Sets"],
    ["appprojects.argoproj.io", "Projects"]]],
  ["Pod Autoscaling", "⇕", [["horizontalpodautoscalers.autoscaling", "HPAs"],
    ["verticalpodautoscalers.autoscaling.k8s.io", "VPAs"], ["scaledobjects.keda.sh", "ScaledObjects"],
    ["scaledjobs.keda.sh", "ScaledJobs"], ["triggerauthentications.keda.sh", "TriggerAuthentications"],
    ["clustertriggerauthentications.keda.sh", "ClusterTriggerAuthentications"]]],
  ["Node Autoscaling", "⬡", [["nodepools.karpenter.sh", "NodePools"], ["ec2nodeclasses.karpenter.k8s.aws", "EC2NodeClasses"],
    ["nodeclaims.karpenter.sh", "NodeClaims"], ["nodeoverlays.karpenter.sh", "NodeOverlays"]]],
];

// Which sections are open, remembered. Default: the cluster list, Workloads and
// whichever section holds the list you are on.
function sectionOpen(name, fallback) {
  const v = store.get("sec." + name);
  return v == null ? fallback : v === "1";
}
function toggleSection(name, open) {
  store.set("sec." + name, open ? "1" : "0");
  const sec = document.querySelector(`[data-sec="${CSS.escape(name)}"]`);
  if (sec) {
    sec.classList.toggle("open", open);
    sec.querySelector(".sec-head").setAttribute("aria-expanded", String(open));
  }
}
function sectionHead(name, icon, count, open) {
  return el("button", { type: "button", class: "sec-head", "aria-expanded": String(open),
      onclick: (e) => { const s = e.currentTarget.parentElement; toggleSection(name, !s.classList.contains("open")); } },
    el("span", { class: "sec-chev", text: "›" }),
    icon ? el("span", { class: "sec-ico", text: icon }) : null,
    el("span", { class: "sec-name", text: name }),
    count != null ? el("span", { class: "sec-count", text: String(count) }) : null);
}

// Quick filters per kind: the questions people actually ask of each list.
const QUICK = {
  pods: [["Restarting", "restarts>0"], ["New (<1h)", "age<1h"], ["Crash / OOM", "status:crash|oom|error"], ["Pending", "status:pending"], ["Hide kube-system", "-ns:kube-system"]],
  "deployments.apps": [["Not ready", "!ok"], ["Scaled to 0", "status:scaleddown"], ["Changed today", "age<1d"]],
  nodes: [["Not ready", "!ok"], ["Cordoned", "status:schedulingdisabled"], ["New (<1d)", "age<1d"]],
  "applications.argoproj.io": [["Out of sync", "sync:outofsync"], ["Degraded", "health:degraded|missing"], ["Progressing", "health:progressing"]],
  events: [["Warnings", "type:warning"], ["Last hour", "age<1h"], ["Scheduling", "reason:failedscheduling"]],
  "poddisruptionbudgets.policy": [["Blocking drains", "status:blocksEviction"], ["No pods", "status:nopods"]],
  "clusterrolebindings.rbac.authorization.k8s.io": [["cluster-admin", "role:cluster-admin"], ["Groups", "subjects:group:"], ["Service accounts", "subjects:serviceaccount:"]],
  "rolebindings.rbac.authorization.k8s.io": [["Service accounts", "subjects:serviceaccount:"], ["Users", "subjects:user:"]],
  "serviceaccounts": [["With IAM role", "-irsa-role:-"], ["Hide default", "-default"]],
  "networkpolicies.networking.k8s.io": [["All pods", "pod-selector:<all>"]],
  "helm:releases": [["Not deployed", "-status:deployed"], ["Changed today", "updated:h ago"], ["Hide kube-system", "-ns:kube-system"]],
  "helm:charts": [["Several versions", "is:warning"]],
  "nodepools.karpenter.sh": [["Not ready", "!ok"]],
};

const FILTER_HELP = [
  ["repo", "name, namespace, status or any column contains it"],
  ["-kube-system", "a leading minus excludes"],
  ['"two words"', "quotes keep a phrase together"],
  ["!ok", "only unhealthy rows"],
  ["is:failing", "health: healthy, warning, failing, unhealthy"],
  ["ns:shop", "field contains: name, ns, status, ctx, or any column"],
  ["status:crash|error", "| means or"],
  ["restarts>3", "numeric compare: > < >= <="],
  ["age<1h", "age with s m h d w"],
  ["app=web", "label equals; tier!=db differs"],
  ["label:team", "label exists"],
  ["node:ip-10-1 restarts>0", "terms combine with AND"],
];

const ctxInfo = (name) => S.contexts.find((c) => c.name === name) || {};
const short = (c) => (c || "").split("/").pop();
const isProtected = (ctx) => !!ctxInfo(ctx).protected;
const kindId = (k) => (k.group ? `${k.name}.${k.group}` : k.name);
const rowKey = (r) => `${r.context}\u0000${r.namespace}\u0000${r.name}`;
const MAX_BULK = 100;
const HUES = [212, 152, 32, 280, 340, 186, 92, 8, 250, 56, 122, 300];
function ctxHue(ctx) {
  let h = 0;
  for (const ch of short(ctx)) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return HUES[h % HUES.length];
}
function ctxTag(ctx, big) {
  const t = el("span", { class: "ctx-tag" + (big ? " big" : ""), style: `--h:${ctxHue(ctx)}`, title: ctx },
    el("span", { class: "ctx-dot" + (isProtected(ctx) ? " prot" : "") }), short(ctx));
  return t;
}

// ------------------------------------------------------------------ theme
// Three states: System (no attribute; CSS follows prefers-color-scheme), Light, Dark.
// Declared before anything that touches them: the theme listeners below run early.
var logSock = null, shellSock = null, term = null, termCleanup = null;

function systemTheme() {
  if (typeof window.__kcSystemDark === "boolean") return window.__kcSystemDark ? "dark" : "light";
  return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}
let themeChoice = "system";
function applyTheme(choice) {
  themeChoice = choice === "light" || choice === "dark" ? choice : "system";
  document.documentElement.dataset.theme = themeChoice === "system" ? systemTheme() : themeChoice;
  store.set("theme", themeChoice === "system" ? null : themeChoice);
  for (const b of document.querySelectorAll("[data-theme-choice]"))
    b.setAttribute("aria-checked", String(b.dataset.themeChoice === themeChoice));
  try { if (term) term.options.theme = termTheme(); } catch (_) {}
  try { if (!$("#welcome").hidden) KVHero.redraw(); } catch (_) {}
  reportState();
}
const currentTheme = () => themeChoice;
function reportState() {
  if (!TOKEN) return;
  let mq = null; try { mq = matchMedia("(prefers-color-scheme: dark)").matches; } catch (_) {}
  fetch("/api/client-state", { method: "POST", headers: { "X-KC-Token": TOKEN, "Content-Type": "application/json" },
    body: JSON.stringify({ choice: themeChoice, applied: document.documentElement.dataset.theme,
      nativeSystemDark: typeof window.__kcSystemDark === "boolean" ? window.__kcSystemDark : null,
      mediaQueryDark: mq, saved: store.get("theme"), ua: navigator.userAgent.slice(0, 120) }) }).catch(() => {});
}
// Follow the system while in System mode: the native window sends kc-system-theme,
// a browser fires the media query's change event.
window.addEventListener("kc-system-theme", () => { if (themeChoice === "system") applyTheme("system"); });
try { matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { if (themeChoice === "system") applyTheme("system"); }); } catch (_) {}

// ------------------------------------------------------------------ boot
async function boot() {
  applyTheme(store.get("theme") || "system");
  for (const b of document.querySelectorAll("[data-theme-choice]")) b.onclick = () => applyTheme(b.dataset.themeChoice);
  if (!TOKEN) {
    $("#tableWrap").replaceChildren(el("div", { class: "empty" }, el("strong", { text: "No session" }),
      "Start the app with kv ui and open the address it prints."));
    return;
  }
  try {
    const r = await api("/api/contexts");
    S.contexts = r.contexts; S.hiddenCtx = r.hidden || []; try { Tabs.prune(S.contexts); } catch (_) {}
    const saved = store.get("ctx");
    S.ctx = saved && ctxInfo(saved).name ? saved : r.current;
  } catch (e) { return toast(e.message, true); }
  fillContextSelect();
  $("#ctx").onchange = () => switchContext($("#ctx").value);
  $("#btnClusters").onclick = showClusters;
  $("#ctxFind").oninput = renderCtxList;
  // When the cluster list is folded, its header still names the open cluster.
  const secOpen = sectionOpen("Clusters", true);
  $("#secClusters").classList.toggle("open", secOpen);
  $("#secClustersHead").setAttribute("aria-expanded", String(secOpen));
  $("#secClustersHead").onclick = () => toggleSection("Clusters", !$("#secClusters").classList.contains("open"));
  $("#ns").onchange = () => { S.ns = $("#ns").value; store.set("ns", S.ns); syncNs(); nsChanged(); };
  wireNsPicker();
  $("#filter").oninput = () => setFilter($("#filter").value, false);
  $("#filterClear").onclick = () => setFilter("");
  $("#filterHelpBtn").onclick = toggleFilterHelp;
  $("#btnPalette").onclick = openPalette;
  $("#btnRefresh").onclick = () => load();
  $("#btnCreate").onclick = () => openCreate();
  $("#btnEverywhere").onclick = () => setEverywhere(!S.everywhere);
  $("#ctxAllBack").onclick = () => setEverywhere(false);
  $("#selClear").onclick = () => { S.picked.clear(); render(); };
  $("#selDelete").onclick = deleteMany;
  $("#selLogs").onclick = () => Dock.logs([...S.picked.values()]);
  Dock.wire();
  $("#panelClose").onclick = closePanel;
  buildFilterHelp();
  wireKeys();
  api("/api/templates").then((t) => KCTemplates.load(t)).catch(() => {});
  setInterval(tickUpdated, 1000);
  S.ns = store.get("ns") || "";
  store.set("filter", null);
  wireWelcome();
  wireSplitters();
  api("/api/files/picker").then((r) => { S.nativePicker = !!r.native; }).catch(() => {});
  showWelcome();
  refreshForwardCount();
}

// ------------------------------------------------------------------ namespace overview
// `kubectl get all -n ns`, complete: every namespaced kind that has something in the
// namespace (Secrets, ConfigMaps, PDBs, CRDs too), one section per kind, each with
// its own columns and health. Events, leases and endpoints are left out unless
// "Include noisy kinds" is ticked.
let ovData = null, ovNoisy = false;
// The empty page shown when the last tab is closed: the start screen's art, no list,
// no refreshing. Opening anything from the sidebar, palette or menu leaves it.
function showEmpty() {
  closePanel();
  stopStreams();
  clearTimeout(S.timer);
  S.empty = true;
  S.overview = false; S.map = false; S.flow = null; S.helm = null;
  for (const id of ["#tableWrap", "#filterArea", "#overview", "#mapView", "#bar", "#selBar", "#errors"]) { const e = $(id); if (e) e.hidden = true; }
  $("#emptyView").hidden = false;
  $("#emptyHint").textContent = `${short(S.ctx)} is still selected. Pick a resource, Topology or Cost in the sidebar, or press ⌘K.`;
  for (const a of document.querySelectorAll("#nav a.on")) a.classList.remove("on");
  for (const n of document.querySelectorAll("#nav a .n")) n.textContent = "";
  S.rows = [];
  try { KVHero.start($("#emptyArt")); } catch (_) {}
}
function leaveEmpty() {
  if (!S.empty) return;
  S.empty = false;
  $("#emptyView").hidden = true;
  $("#bar").hidden = false; $("#tableWrap").hidden = false; $("#filterArea").hidden = false;
  try { KVHero.stop(); } catch (_) {}
}

function leaveOverview() {
  leaveEmpty();
  if (!S.overview && !S.map) return;
  S.overview = false; S.map = false; S.flow = null;
  $("#overview").hidden = true;
  $("#mapView").hidden = true;
  $("#tableWrap").hidden = false;
  $("#filterArea").hidden = false;
}
async function showOverview(ns) {
  closePanel();
  S.helm = null;
  leaveOverview();
  S.overview = true;
  clearTimeout(S.timer);
  if (ns) { S.ns = ns; $("#ns").value = ns; store.set("ns", ns); syncNs(); }
  $("#tableWrap").hidden = true;
  $("#filterArea").hidden = true;
  $("#overview").hidden = false;
  $("#crumbKind").textContent = "Namespace Overview";
  markNav();
  const box = $("#overview");
  if (!S.ns || nsChosen().length > 1) {
    let names = nsChosen().length > 1 ? nsChosen() : [];
    if (!names.length) try { names = await api("/api/namespaces?" + qs({ context: S.ctx })); } catch (_) {}
    box.replaceChildren(el("div", { class: "ov-pick" },
      el("h2", { text: "Namespace Overview" }),
      el("p", { class: "muted", text: "Choose a namespace to see every resource in it, grouped by kind — like kubectl get all -n, but covering every kind, not just eight." }),
      el("div", { class: "ov-ns" }, names.map((n) => el("button", { type: "button", text: n, onclick: () => showOverview(n) })))));
    $("#crumbWhere").textContent = short(S.ctx);
    return;
  }
  $("#crumbWhere").textContent = `${short(S.ctx)} · ${S.ns} · loading…`;
  box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: `Loading ${S.ns}…` }), "Every resource type is queried in parallel."));
  setSpin(true);
  const t0 = performance.now();
  try { ovData = await api("/api/everything?" + qs({ context: S.ctx, namespace: S.ns, noisy: ovNoisy })); }
  catch (e) { setSpin(false); return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not list the namespace" }), e.message)); }
  setSpin(false);
  if (!S.overview) return;
  renderOverview(Math.round(performance.now() - t0));
}
function renderOverview(ms) {
  const box = $("#overview");
  const ok = ovData.filter((k) => !k.error);
  const denied = ovData.filter((k) => k.error);
  const total = ok.reduce((n, k) => n + k.rows.length, 0);
  const bad = ok.reduce((n, k) => n + k.rows.filter((r) => r.health === "bad").length, 0);
  const warn = ok.reduce((n, k) => n + k.rows.filter((r) => r.health === "warn").length, 0);
  $("#crumbWhere").textContent = `${short(S.ctx)} · ${S.ns} · ${total} resources in ${ok.length} kinds`;
  const find = el("input", { id: "ovFind", placeholder: "Filter, e.g. web, !ok, app=web", value: S.ovFind || "",
    oninput: () => { S.ovFind = find.value; drawSections(); } });
  const noisy = el("label", { class: "muted" }, el("input", { type: "checkbox", checked: ovNoisy,
    onchange: (e) => { ovNoisy = e.target.checked; showOverview(); } }), " Include events, endpoints and leases");
  const jump = el("div", { class: "ov-jump" });
  const sections = el("div", { class: "ov-sections" });
  box.replaceChildren(
    el("div", { class: "ov-bar" },
      el("div", { class: "ov-stats" },
        el("span", {}, el("b", { text: String(total) }), " resources"),
        el("span", {}, el("b", { text: String(ok.length) }), " kinds"),
        bad ? el("span", { class: "bad" }, el("b", { text: String(bad) }), " failing") : null,
        warn ? el("span", { class: "warn" }, el("b", { text: String(warn) }), " warning") : null,
        el("span", { class: "muted", text: ms ? `listed in ${(ms / 1000).toFixed(1)}s` : "" })),
      find, noisy,
      el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showOverview() })),
    jump, sections,
    denied.length ? el("details", { class: "ov-denied" }, el("summary", { text: `${denied.length} kinds could not be listed (no permission or an API that is down)` }),
      el("pre", { text: denied.map((k) => `${kindId(k.kind)}: ${k.error.split("\n")[0]}`).join("\n") })) : null);
  function drawSections() {
    const f = KCFilter.parse(S.ovFind || "");
    const parts = [];
    const chips = [];
    for (const k of ok) {
      const kind = { ...k.kind, id: kindId(k.kind) };
      const rows = k.rows.filter((r) => f.match({ ...r, context: S.ctx }));
      if (!rows.length) continue;
      const kb = rows.filter((r) => r.health === "bad").length, kw = rows.filter((r) => r.health === "warn").length;
      const anchor = "ov-" + kind.id.replace(/[^a-z0-9]/gi, "-");
      chips.push(el("a", { href: "#", class: "ov-chip" + (kb ? " bad" : kw ? " warn" : ""), onclick: (e) => { e.preventDefault();
        document.getElementById(anchor)?.scrollIntoView({ behavior: "smooth", block: "start" }); } },
        kind.kind, el("b", { text: String(rows.length) })));
      const cols = k.columns || [];
      const hasStatus = rows.some((r) => r.status && r.status !== "-");
      const head = ["NAME", ...(hasStatus ? ["STATUS"] : []), ...cols, "AGE"];
      const open = sectionOpen("ov:" + kind.id, true);
      parts.push(el("section", { class: "ov-sec" + (open ? " open" : ""), id: anchor, "data-sec": "ov:" + kind.id },
        el("button", { type: "button", class: "ov-head", onclick: (e) => { const s = e.currentTarget.parentElement; const o = !s.classList.contains("open"); s.classList.toggle("open", o); store.set("sec.ov:" + kind.id, o ? "1" : "0"); } },
          el("span", { class: "sec-chev", text: "›" }), el("span", { class: "ov-kind", text: kind.kind }),
          el("span", { class: "muted mono", text: kind.id }), el("span", { class: "grow" }),
          kb ? el("span", { class: "pill bad", text: `${kb} failing` }) : null, kw ? el("span", { class: "pill warn", text: `${kw} warning` }) : null,
          el("span", { class: "ov-n", text: String(rows.length) }),
          el("span", { class: "ov-open", title: `Open the ${kind.kind} list`, text: "Open list →",
            onclick: (e) => { e.stopPropagation(); setKind(kind.id); } })),
        el("div", { class: "ov-body" }, el("table", {},
          el("thead", {}, el("tr", {}, head.map((h) => el("th", { text: h })))),
          el("tbody", {}, rows.map((r) => el("tr", { onclick: () => { S.kind = kind; openPanel({ ...r, context: S.ctx }); } },
            el("td", { class: "name", text: r.name, title: r.name }),
            hasStatus ? el("td", {}, el("span", { class: "pill " + (r.health || ""), text: r.status || "—" })) : null,
            ...cols.map((c) => el("td", { class: "mono", text: (r.cells || {})[c] ?? "", title: (r.cells || {})[c] ?? "" })),
            el("td", { class: "mono num", text: r.age }))))))));
    }
    jump.replaceChildren(...chips);
    sections.replaceChildren(...(parts.length ? parts : [el("div", { class: "empty" }, el("strong", { text: "Nothing matches" }), "Clear the filter.")]));
  }
  drawSections();
}

// ------------------------------------------------------------------ files
// Copy to and from a pod or a node, like kubectl cp: browse a directory, download a
// file (or a whole directory as .tar), upload files into the directory in view.
// Pods need tar and sh in the image. Nodes go through the node-shell helper pod
// (the host's / is at /host there), started for each operation and removed after.
async function showFiles(row, kind, body) {
  const isNode = kind.id === "nodes";
  let containers = [], container = "";
  if (!isNode) {
    containers = await api("/api/containers?" + qs({ context: row.context, namespace: row.namespace, name: row.name }));
    container = containers[containers.length - 1];
  }
  let cwd = isNode ? "/var/log" : "/";
  const target = () => isNode ? { context: row.context, node: row.name } : { context: row.context, namespace: row.namespace, name: row.name, container };
  const pathIn = el("input", { id: "filesPath", class: "mono", value: cwd, spellcheck: "false", "aria-label": "Path",
    onkeydown: (e) => { if (e.key === "Enter") go(pathIn.value); } });
  const sel = containers.length > 1 ? el("select", { "aria-label": "Container", onchange: () => { container = sel.value; go(cwd); } },
    containers.map((c) => el("option", { value: c, text: c }))) : null;
  if (sel) sel.value = container;
  const fileIn = el("input", { type: "file", multiple: true, id: "filesUpload", class: "visually-hidden",
    onchange: () => upload([...fileIn.files]) });
  const list = el("div", { class: "files" });
  const crumbs = el("div", { class: "files-crumbs" });
  const note = el("div", { class: "muted files-note", text: isNode
    ? "Node files are read through a short-lived helper pod; the first listing takes a few seconds." : "" });
  const drop = el("div", { class: "pane files-pane",
    ondragover: (e) => { e.preventDefault(); drop.classList.add("over"); },
    ondragleave: () => drop.classList.remove("over"),
    ondrop: (e) => { e.preventDefault(); drop.classList.remove("over"); if (e.dataTransfer.files.length) upload([...e.dataTransfer.files]); } },
    el("div", { class: "tools" }, sel, pathIn,
      el("button", { type: "button", text: "Go", onclick: () => go(pathIn.value) }),
      el("button", { type: "button", class: "ghost", text: "↑ Up", onclick: () => go(cwd.replace(/\/[^/]+\/?$/, "") || "/") }),
      el("span", { class: "grow" }),
      el("button", { type: "button", text: "Download folder", title: "This directory as a .tar", onclick: () => download(cwd) }),
      el("label", { for: "filesUpload", class: "btn primary", role: "button", tabindex: "0",
        onclick: (e) => { if (S.nativePicker) { e.preventDefault(); uploadPicked(); } },
        onkeydown: (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); S.nativePicker ? uploadPicked() : fileIn.click(); } } }, "Upload…"), fileIn),
    crumbs, note, list,
    el("div", { class: "files-drop", text: "Drop files here to upload them into this folder" }));
  body.replaceChildren(drop);

  async function go(p) {
    cwd = ("/" + (p || "/").replace(/^\/+/, "")).replace(/\/+$/, "") || "/";
    pathIn.value = cwd;
    const parts = cwd.split("/").filter(Boolean);
    crumbs.replaceChildren(el("a", { href: "#", text: isNode ? "host /" : "/", onclick: (e) => { e.preventDefault(); go("/"); } }),
      ...parts.map((seg, i) => [el("span", { class: "muted", text: " / " }),
        el("a", { href: "#", text: seg, onclick: (e) => { e.preventDefault(); go("/" + parts.slice(0, i + 1).join("/")); } })]).flat());
    list.replaceChildren(el("div", { class: "empty", text: "Loading…" }));
    let entries;
    try { entries = await api("/api/files?" + qs({ ...target(), path: cwd })); }
    catch (e) { return list.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Can't list this folder" }), e.message)); }
    if (!entries || !entries.length) return list.replaceChildren(el("div", { class: "empty", text: "Empty folder" }));
    const size = (n) => n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`;
    list.replaceChildren(el("table", { class: "files-table" },
      el("thead", {}, el("tr", {}, ["NAME", "SIZE", "MODIFIED", ""].map((h) => el("th", { text: h })))),
      el("tbody", {}, entries.map((f) => el("tr", { ondblclick: () => f.dir ? go(cwd + "/" + f.name) : download(cwd + "/" + f.name) },
        el("td", {}, el("span", { class: "file-ico", text: f.dir ? "▸" : f.link ? "↪" : "·" }),
          f.dir ? el("a", { href: "#", text: f.name, onclick: (e) => { e.preventDefault(); go(cwd + "/" + f.name); } }) : el("span", { text: f.name }),
          f.link ? el("span", { class: "muted", text: " → " + f.link }) : null),
        el("td", { class: "mono num", text: f.dir ? "" : size(f.size) }),
        el("td", { class: "mono", text: f.modTime && !f.modTime.startsWith("0001") ? new Date(f.modTime).toLocaleString() : "" }),
        el("td", { class: "num" }, el("button", { type: "button", class: "ghost small", text: f.dir ? "Download .tar" : "Download",
          onclick: () => download(cwd + "/" + f.name) })))))));
  }

  async function download(p) {
    toast(`Downloading ${p}…`);
    try {
      const r = await api("/api/files/save?" + qs({ ...target(), path: p }), { method: "POST", body: "{}" });
      const kb = r.size < 1048576 ? `${(r.size / 1024).toFixed(1)} KB` : `${(r.size / 1048576).toFixed(1)} MB`;
      showSaved(r.path, `Saved ${r.name} (${kb}) to Downloads`);
    } catch (e) { toast(`Download failed: ${e.message}`, true); }
  }

  // In the app's own window, the Mac's Open panel is shown by the app itself (the web
  // view's file input doesn't open there); the server reads the chosen files.
  async function uploadPicked() {
    let confirm = "";
    if (isProtected(row.context)) {
      confirm = await ask({ title: `Upload files into ${row.name}?`,
        text: `Into ${cwd} on a protected cluster. Files with the same name are overwritten. The file chooser opens next.`,
        row, want: row.name, ok: "Choose files…" });
      if (confirm == null) return;
    }
    try {
      const r = await api("/api/files/upload-picked?" + qs({ ...target(), path: cwd }), { method: "POST", body: JSON.stringify({ confirm }) });
      if (r.cancelled) return;
      toast(r.ok.charAt(0).toUpperCase() + r.ok.slice(1));
      go(cwd);
    } catch (e) { toast(`Upload failed: ${e.message}`, true); }
  }

  async function upload(files) {
    if (!files.length) return;
    const total = files.reduce((n, f) => n + f.size, 0);
    if (total > 256 * 1048576) return toast("Upload at most 256 MB at a time.", true);
    let confirm = "";
    const who = row.name;
    if (isProtected(row.context)) {
      confirm = await ask({ title: `Copy ${files.length} file${files.length === 1 ? "" : "s"} into ${who}?`,
        text: `Into ${cwd} on a protected cluster. Files with the same name are overwritten.`, row, want: who, ok: "Upload" });
      if (confirm == null) return;
    }
    const enc = {};
    for (const f of files) {
      const buf = new Uint8Array(await f.arrayBuffer());
      let s = ""; for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
      enc[f.name] = btoa(s);
    }
    toast(`Uploading ${files.length} file${files.length === 1 ? "" : "s"}…`);
    try {
      const r = await api("/api/files/upload?" + qs({ ...target(), path: cwd }), { method: "POST", body: JSON.stringify({ confirm, files: enc }) });
      toast(r.ok.charAt(0).toUpperCase() + r.ok.slice(1));
      go(cwd);
    } catch (e) { toast(`Upload failed: ${e.message}`, true); }
    fileIn.value = "";
  }

  go(cwd);
}

// ------------------------------------------------------------------ create
// "+ Create": a YAML editor in the drawer, pre-filled with a template for the list in
// view (or any picked from the menu), or with whatever you paste. Validate runs a
// server-side dry run -- the API server checks schema, admission and quotas without
// storing anything; Create stores it. Several documents (---) and kind: List work.
function openCreate(forKind) {
  stopStreams();
  S.panel = null;
  const target = S.ctx;
  // (not "kindId": that would shadow the kindId() helper used below)
  const kind = forKind ? (findKind(forKind) ? { ...findKind(forKind), id: forKind } : null) : S.helm || S.overview ? null : S.kind;
  const ns = nsChosen()[0] || (S.contexts.find((c) => c.name === target) || {}).namespace || "default";
  $("#panel").hidden = false;
  $("#panelKind").replaceChildren(ctxTag(target, true), " Create");
  $("#panelTitle").textContent = kind ? `New ${kind.kind}` : "New resource";
  $("#panelTabs").replaceChildren();
  $("#panelActions").replaceChildren();
  const ta = el("textarea", { class: "code", spellcheck: "false", id: "createYaml", "aria-label": "YAML to create" });
  ta.value = KCTemplates.forKind(kind && KCTemplates.has(kind.id) ? kind : kind || { id: "deployments.apps" }, ns);
  const pick = el("select", { id: "createTemplate", "aria-label": "Template" },
    el("option", { value: "", text: "Template…" }),
    KCTemplates.MENU.filter(([id]) => S.kinds.some((k) => kindId(k) === id)).map(([id, label]) => el("option", { value: id, text: label })));
  pick.onchange = () => {
    if (!pick.value) return;
    const k = findKind(pick.value);
    if (ta.value.trim() && ta.dataset.edited === "1") {
      ask({ title: "Replace the editor's YAML?", text: "Your edits will be lost.", ok: "Replace" }).then((v) => {
        if (v != null) { ta.value = KCTemplates.forKind({ ...k, id: pick.value }, ns); ta.dataset.edited = "0"; }
        pick.value = "";
      });
      return;
    }
    ta.value = KCTemplates.forKind({ ...k, id: pick.value }, ns); ta.dataset.edited = "0"; pick.value = "";
  };
  ta.oninput = () => { ta.dataset.edited = "1"; };
  // Tab inserts two spaces instead of leaving the editor.
  ta.onkeydown = (e) => {
    if (e.key === "Tab" && !e.shiftKey) { e.preventDefault(); const s = ta.selectionStart; ta.setRangeText("  ", s, ta.selectionEnd, "end"); }
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") { e.preventDefault(); run(false); }
  };
  const result = el("div", { class: "create-result", "aria-live": "polite" });
  const nsNote = el("span", { class: "muted", text: `Edit the template or paste your own YAML (several objects with ---). No namespace → ${ns}.` });
  const validate = el("button", { type: "button", text: "Validate", title: "Dry run on the server: nothing is created" });
  const create = el("button", { type: "button", class: "primary", text: "Create", title: "⌘↵" });
  async function run(dry) {
    let confirm = "";
    if (!dry && isProtected(target)) {
      confirm = await ask({ title: `Create on ${short(target)}?`, text: "This cluster is protected.", want: short(target), ok: "Create" });
      if (confirm == null) return;
    }
    validate.disabled = create.disabled = true;
    result.replaceChildren(el("span", { class: "muted", text: dry ? "Validating on the server…" : "Creating…" }));
    try {
      const res = await post("/api/create", { context: target, namespace: ns, yaml: ta.value, dryRun: dry, confirm });
      const bad = res.filter((r) => !r.ok);
      result.replaceChildren(
        el("div", { class: "create-sum " + (bad.length ? "bad" : "ok"),
          text: bad.length ? `${bad.length} of ${res.length} ${dry ? "would fail" : "failed"}` : dry ? `Valid — ${res.length} object${res.length === 1 ? "" : "s"} would be created` : `Created ${res.length} object${res.length === 1 ? "" : "s"}` }),
        el("ul", {}, res.map((r) => el("li", { class: r.ok ? "ok" : "bad" },
          el("span", { class: "mono", text: `${r.kind} ${r.namespace ? r.namespace + "/" : ""}${r.name}` }),
          r.ok ? el("span", { class: "muted", text: dry ? " ok" : " created" }) : el("span", { class: "err-line", text: " " + r.error })))));
      if (!dry && !bad.length) { toast(`Created ${res.length} object${res.length === 1 ? "" : "s"}`); ta.dataset.edited = "0"; load(); }
    } catch (e) {
      result.replaceChildren(el("div", { class: "create-sum bad", text: e.message }));
    } finally { validate.disabled = create.disabled = false; }
  }
  validate.onclick = () => run(true);
  create.onclick = () => run(false);
  $("#panelBody").replaceChildren(el("div", { class: "pane" },
    el("div", { class: "tools" }, pick, nsNote, el("span", { class: "grow" }), validate, create),
    result, ta));
  setTimeout(() => ta.focus(), 0);
}

// ------------------------------------------------------------------ resource map
// One namespace drawn as a map (graph.js): Ingress → Service → workload (pods as dots)
// → ConfigMap / Secret / volume. Hover a box to light up its chain, click to pin,
// double-click to open it. Broken links show: an Ingress pointing at a Service that
// doesn't exist, a Service that selects no pods, a Secret a workload needs but lacks.
// scope "cluster" draws the whole cluster (node pools ↔ namespaces); "namespace"
// draws one namespace. Without a scope it keeps the last one.
async function showMap(ns, scope) {
  clearTimeout(S.evTimer);
  closePanel();
  S.flow = null;
  S.helm = null;
  leaveOverview();
  S.map = true;
  clearTimeout(S.timer);
  if (ns) { S.ns = ns; $("#ns").value = ns; store.set("ns", ns); syncNs(); if (!["cost", "helm", "argo", "events"].includes(scope)) scope = "namespace"; }
  S.mapScope = scope || S.mapScope || store.get("mapScope") || "cluster";
  store.set("mapScope", S.mapScope);
  $("#tableWrap").hidden = true;
  $("#filterArea").hidden = true;
  $("#mapView").hidden = false;
  $("#crumbKind").textContent = { cluster: "Cluster Topology", helm: "Helm Topology", cost: "Cost", argo: "Argo CD", events: "Warnings" }[S.mapScope] || "Namespace Topology";
  const secName = S.mapScope === "cost" ? "Cost" : S.mapScope === "argo" ? "Argo CD" : S.mapScope === "events" ? "Cluster" : "Topology";
  const mapSec = document.querySelector(`[data-sec="${secName}"]`);
  if (mapSec && !mapSec.classList.contains("open")) toggleSection(secName, true);
  markNav();
  const box = $("#mapView");
  if (S.mapScope === "cluster") return showClusterMap(box);
  if (S.mapScope === "helm") return showHelmMap(box);
  if (S.mapScope === "cost") return showCost(box);
  if (S.mapScope === "argo") return showArgo(box);
  if (S.mapScope === "events") return showEvents(box);
  if (!S.ns || nsChosen().length > 1) {
    let names = nsChosen().length > 1 ? nsChosen() : [];
    if (!names.length) try { names = await api("/api/namespaces?" + qs({ context: S.ctx })); } catch (_) {}
    box.replaceChildren(el("div", { class: "ov-pick" },
      el("h2", { text: "Namespace Topology" }),
      el("p", { class: "muted", text: "Choose a namespace to see how its traffic, workloads and configuration connect — or see the whole cluster's topology." }),
      el("p", {}, el("button", { type: "button", class: "primary", text: "Cluster Topology", onclick: () => showMap(null, "cluster") })),
      el("div", { class: "ov-ns" }, names.map((n) => el("button", { type: "button", text: n, onclick: () => showMap(n) })))));
    $("#crumbWhere").textContent = short(S.ctx);
    return;
  }
  $("#crumbWhere").textContent = `${short(S.ctx)} · ${S.ns} · loading…`;
  box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: `Mapping ${S.ns}…` })));
  setSpin(true);
  let g;
  try { g = await api("/api/graph?" + qs({ context: S.ctx, namespace: S.ns })); }
  catch (e) { setSpin(false); return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not map the namespace" }), e.message)); }
  setSpin(false);
  if (!S.map) return;
  const problems = g.nodes.filter((n) => n.health === "bad" || n.health === "warn");
  const pods = g.nodes.reduce((a, n) => a + (n.podsAll || 0), 0);
  $("#crumbWhere").textContent = `${short(S.ctx)} · ${S.ns} · ${g.nodes.length} resources · ${pods} pods`;
  const canvas = el("div", { class: "map-canvas" });
  const legend = el("div", { class: "map-legend" },
    el("span", {}, el("i", { class: "lg-dot ok" }), "healthy"), el("span", {}, el("i", { class: "lg-dot warn" }), "warning"),
    el("span", {}, el("i", { class: "lg-dot bad" }), "failing / missing"), el("span", {}, el("i", { class: "lg-dot dash" }), "referenced, absent"),
    el("span", { class: "muted", text: "Click a resource for its own topology, top to bottom · double-click to open" }));
  const issues = problems.length ? el("div", { class: "map-issues" }, el("strong", { text: `${problems.length} to look at:` }),
    ...problems.slice(0, 8).map((n) => el("button", { type: "button", class: "chip-btn h-" + n.health, text: `${n.kind} ${n.name} · ${n.status}`,
      onclick: () => view.focus(n.id) }))) : null;
  box.replaceChildren(...[el("div", { class: "map-bar" }, mapScopeSwitch(), legend,
      el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showMap() })),
    issues, g.warnings && g.warnings.length ? el("div", { class: "muted map-warn", text: "Not shown: " + g.warnings.join("; ") }) : null,
    canvas].filter(Boolean));
  $("#crumbKind").textContent = "Namespace Topology";
  if (!g.nodes.length) return canvas.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Nothing to map" }), "This namespace has no workloads, services or ingresses."));
  const onOpen = (n) => {
    if (!n.kindId || n.missing) return toast(n.missing ? `${n.kind} ${n.name} doesn't exist in ${S.ns}.` : `${n.kind} is an operator's resource; find it under Custom Resources.`);
    const k = findKind(n.kindId);
    if (!k) return;
    S.kind = { ...k, id: kindId(k) };
    openPanel({ context: S.ctx, namespace: S.ns, name: n.name, status: n.status, health: n.health, cells: {}, labels: {} });
  };
  // Clicking a resource opens its own view: just its chain, top to bottom. "All of
  // <namespace>" (or Esc) goes back to the whole map.
  const bar = box.querySelector(".map-bar");
  const back = el("button", { type: "button", class: "ghost small map-back", hidden: true, text: `← All of ${S.ns}` });
  bar.prepend(back);
  const crumbAll = $("#crumbWhere").textContent;
  let view;
  const whole = () => {
    back.hidden = true; S.flow = null;
    if (issues) issues.hidden = false;
    $("#crumbWhere").textContent = crumbAll;
    view = KCGraph.render(canvas, g, { onOpen, onSelect: (n) => flow(n) });
  };
  const flow = (n) => {
    S.flow = { back: whole };
    back.hidden = false;
    if (issues) issues.hidden = true;
    $("#crumbWhere").textContent = `${short(S.ctx)} · ${S.ns} · ${n.kind} ${n.name}`;
    KCGraph.renderFlow(canvas, g, n.id, { onOpen, onSelect: (m) => flow(m) });
    canvas.scrollTop = 0;
  };
  back.onclick = whole;
  whole();
  if (issues) for (const b of issues.querySelectorAll("button")) {
    const node = g.nodes.find((n) => b.textContent.startsWith(`${n.kind} ${n.name} ·`));
    if (node) b.onclick = () => flow(node);
  }
}

// Cluster / Namespace toggle shown in both maps' bars.
function mapScopeSwitch() {
  const b = (scope, label) => el("button", { type: "button", role: "radio", "aria-checked": String(S.mapScope === scope), text: label,
    onclick: () => { if (S.mapScope !== scope) showMap(null, scope); } });
  return el("div", { class: "seg map-scope", role: "radiogroup", "aria-label": "Map scope" },
    b("cluster", "Cluster"), b("namespace", S.ns ? `Namespace · ${S.ns}` : "Namespace"), b("helm", "Helm"));
}

// The whole cluster: node pools (with every node) on one side, namespaces on the
// other, a band for the pods each namespace runs on each pool. Click a namespace
// to open its map.
async function showClusterMap(box) {
  const ctx = S.ctx;
  $("#crumbWhere").textContent = `${short(ctx)} · loading…`;
  box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: `Mapping ${short(ctx)}…` }), "Reading nodes and pods in every namespace."));
  setSpin(true);
  let cm;
  try { cm = await api("/api/clustermap?" + qs({ context: ctx })); }
  catch (e) { setSpin(false); return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not map the cluster" }), e.message)); }
  setSpin(false);
  if (!S.map || S.mapScope !== "cluster" || S.ctx !== ctx) return;
  const draw = () => {
    const showSystem = store.get("mapSystem") === "1";
    const nss = cm.namespaces.filter((n) => showSystem || !n.system);
    const bad = cm.namespaces.filter((n) => n.health === "bad" || n.health === "warn");
    const badNodes = cm.pools.flatMap((p) => p.nodes.filter((n) => n.health !== "ok").map((n) => ({ ...n, pool: p.name })));
    $("#crumbKind").textContent = "Cluster Topology";
    $("#crumbWhere").textContent = `${short(ctx)} · ${cm.nodes} nodes · ${cm.pools.length} pools · ${cm.namespaces.length} namespaces · ${cm.pods} pods`;
    const cpuReq = cm.pools.reduce((a, p) => a + p.cpuReq, 0), cpuAlloc = cm.pools.reduce((a, p) => a + p.cpuAlloc, 0);
    const memReq = cm.pools.reduce((a, p) => a + p.memReq, 0), memAlloc = cm.pools.reduce((a, p) => a + p.memAlloc, 0);
    const pct = (a, b) => (b ? Math.round((a * 100) / b) : 0);
    const stat = (label, value, sub, cls) => el("div", { class: "cm-stat" + (cls ? " " + cls : "") },
      el("span", { class: "cm-stat-l", text: label }), el("strong", { text: value }), sub ? el("span", { class: "cm-stat-s", text: sub }) : null);
    const stats = el("div", { class: "cm-stats" },
      stat("Nodes", String(cm.nodes), `${cm.pools.length} pool${cm.pools.length === 1 ? "" : "s"}${badNodes.length ? ` · ${badNodes.length} not ready` : ""}`, badNodes.length ? "warn" : ""),
      stat("Pods", String(cm.pods), `${cm.namespaces.length} namespaces`),
      stat("CPU requested", pct(cpuReq, cpuAlloc) + "%", `${(cpuReq / 1000).toFixed(1)} of ${(cpuAlloc / 1000).toFixed(0)} cores`, pct(cpuReq, cpuAlloc) >= 85 ? "warn" : ""),
      stat("Memory requested", pct(memReq, memAlloc) + "%", `${Math.round(memReq / 2 ** 30)} of ${Math.round(memAlloc / 2 ** 30)} GiB`, pct(memReq, memAlloc) >= 85 ? "warn" : ""),
      stat("Ingresses", String(cm.ingresses), ""),
      stat("Needs a look", String(bad.length), bad.length ? "namespaces with failing or unready pods" : "every namespace healthy", bad.length ? "bad" : "ok"));
    const sysToggle = el("label", { class: "cm-toggle" },
      el("input", { type: "checkbox", checked: showSystem, onchange: (e) => { store.set("mapSystem", e.target.checked ? "1" : null); draw(); } }),
      `Platform namespaces (${cm.namespaces.length - cm.namespaces.filter((n) => !n.system).length})`);
    const legend = el("div", { class: "map-legend" },
      el("span", {}, el("i", { class: "lg-sq cool" }), "node, CPU <60%"), el("span", {}, el("i", { class: "lg-sq warm" }), "60–90%"),
      el("span", {}, el("i", { class: "lg-sq hot" }), "≥90%"), el("span", {}, el("i", { class: "lg-sq bad" }), "not ready"),
      el("span", { class: "muted", text: "Band width = pods · hover to trace · click a namespace for its topology" }));
    const issues = bad.length || badNodes.length ? el("div", { class: "map-issues" }, el("strong", { text: "To look at:" }),
      ...badNodes.slice(0, 4).map((n) => el("button", { type: "button", class: "chip-btn h-" + n.health, text: `node ${shortNode(n.name)} · ${n.status}`,
        onclick: () => { setKind("nodes"); setTimeout(() => setFilter(n.name), 300); } })),
      ...bad.slice(0, 8).map((n) => el("button", { type: "button", class: "chip-btn h-" + n.health,
        text: `${n.name} · ${n.failing ? n.failing + " failing" : n.warning + " not ready"}`, onclick: () => showMap(n.name) }))) : null;
    const canvas = el("div", { class: "map-canvas" });
    box.replaceChildren(...[el("div", { class: "map-bar" }, mapScopeSwitch(), legend, sysToggle,
        el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showMap(null, "cluster") })),
      stats, issues, cm.warnings && cm.warnings.length ? el("div", { class: "muted map-warn", text: "Not shown: " + cm.warnings.join("; ") }) : null,
      canvas].filter(Boolean));
    if (!nss.length && !cm.pools.length) return canvas.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Nothing to map" }), "No nodes or pods were readable."));
    KCGraph.renderCluster(canvas, cm, { showSystem,
      onNamespace: (n) => showMap(n),
      onPool: (p) => { setKind("nodes"); setTimeout(() => setFilter(p.source === "karpenter" ? "karpenter.sh/nodepool=" + p.name : p.name), 300); } });
  };
  draw();
}
const shortNode = (n) => n.replace(/\.(ec2|compute)\.internal$|\..*\.compute\.internal$/, "");

// Helm Topology: each release on the left, a band to every resource its manifest
// created, grouped by kind on the right, with the resource's live state. A release is
// red when something it made is failing or missing (deleted by hand, half-installed).
async function showHelmMap(box) {
  const ctx = S.ctx, ns = S.ns;
  $("#crumbWhere").textContent = `${short(ctx)} · ${ns || "all namespaces"} · loading…`;
  box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Reading Helm releases…" }), "Every release's manifest, checked against what is in the cluster."));
  setSpin(true);
  let hm;
  try { hm = await api("/api/helmmap?" + qs({ context: ctx, namespace: ns })); }
  catch (e) { setSpin(false); return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not read Helm releases" }), e.message)); }
  setSpin(false);
  if (!S.map || S.mapScope !== "helm" || S.ctx !== ctx) return;
  const total = hm.releases.reduce((a, r) => a + r.counts.Total, 0);
  const bad = hm.releases.filter((r) => r.health === "bad" || r.health === "warn");
  $("#crumbKind").textContent = "Helm Topology";
  $("#crumbWhere").textContent = `${short(ctx)} · ${ns || "all namespaces"} · ${hm.releases.length} releases · ${total} resources`;
  let sel = store.get("helmmapSel");
  if (!hm.releases.some((r) => r.namespace + "/" + r.name === sel)) sel = (bad[0] || hm.releases[0] || {}).namespace + "/" + (bad[0] || hm.releases[0] || {}).name;
  const find = el("input", { placeholder: "Find a release or resource", class: "hm-find", spellcheck: "false", "aria-label": "Find" });
  const list = el("div", { class: "hm-list", role: "listbox", "aria-label": "Helm releases" });
  const canvas = el("div", { class: "map-canvas hm-canvas" });
  const legend = el("div", { class: "map-legend" },
    el("span", {}, el("i", { class: "lg-dot ok" }), "healthy"), el("span", {}, el("i", { class: "lg-dot warn" }), "warning"),
    el("span", {}, el("i", { class: "lg-dot bad" }), "failing / missing"),
    el("span", { class: "muted", text: "Pick a release · double-click a resource to open it" }));
  box.replaceChildren(...[el("div", { class: "map-bar" }, mapScopeSwitch(), legend,
      el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showMap(null, "helm") })),
    hm.warnings && hm.warnings.length ? el("div", { class: "muted map-warn", text: "Not checked: " + hm.warnings.join("; ") }) : null,
    el("div", { class: "hm-wrap" }, el("div", { class: "hm-side" }, find, list), canvas)].filter(Boolean));
  if (!hm.releases.length) return canvas.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "No Helm releases" }), ns ? `Nothing installed with Helm in ${ns}.` : "Nothing in this cluster was installed with Helm."));
  const drawList = () => {
    const q = find.value.trim().toLowerCase();
    const shown = hm.releases.filter((r) => !q || (r.namespace + "/" + r.name + " " + r.chart).toLowerCase().includes(q) ||
      r.objects.some((o) => (o.kind + " " + o.name).toLowerCase().includes(q)));
    list.replaceChildren(...shown.map((r) => {
      const key = r.namespace + "/" + r.name;
      const issues = r.counts.Missing + r.counts.Failing + r.counts.Warning;
      return el("button", { type: "button", role: "option", class: "hm-item h-" + (r.health || "none") + (key === sel ? " on" : ""), "aria-selected": String(key === sel),
          onclick: () => { sel = key; store.set("helmmapSel", key); drawList(); drawRelease(); } },
        el("span", { class: "hm-led" }),
        el("span", { class: "hm-name" }, el("strong", { text: r.name }), el("span", { class: "muted", text: `${r.namespace} · ${r.chart}-${r.chartVersion}` })),
        el("span", { class: "hm-count" + (issues ? " bad" : ""), text: issues ? `${issues} ✕` : String(r.counts.Total) }));
    }));
    if (!shown.length) list.append(el("div", { class: "muted hm-none", text: "Nothing matches" }));
  };
  const drawRelease = () => {
    const r = hm.releases.find((x) => x.namespace + "/" + x.name === sel);
    if (!r) return canvas.replaceChildren();
    const q = find.value.trim().toLowerCase();
    KCGraph.renderHelm(canvas, r, {
      highlight: q,
      onOpen: (o) => {
        if (o.missing) return toast(`${o.kind} ${o.name} is in the release's manifest but not in the cluster.`, true);
        if (!o.kindId) return toast(`${o.kind} isn't served by this cluster.`, true);
        const k = findKind(o.kindId);
        if (!k) return;
        S.kind = { ...k, id: kindId(k) };
        openPanel({ context: S.ctx, namespace: o.namespace, name: o.name, status: o.status, health: o.health, cells: {}, labels: {} });
      },
      onRelease: () => openHelm({ context: S.ctx, namespace: r.namespace, name: r.name, status: r.status, health: r.health, cells: {} }),
    });
  };
  find.oninput = () => { drawList(); drawRelease(); };
  drawList();
  // Lay out after the canvas has its width (the grid sizes it), and again on resize.
  requestAnimationFrame(drawRelease);
  let lastW = 0, rt;
  const ro = new ResizeObserver(() => {
    if (!canvas.isConnected) return ro.disconnect();
    const w = canvas.clientWidth;
    if (Math.abs(w - lastW) < 40) return;
    lastW = w; clearTimeout(rt); rt = setTimeout(drawRelease, 120);
  });
  ro.observe(canvas);
}

// ------------------------------------------------------------------ cost
// Spend from the cluster's OpenCost (or Kubecost): by namespace, workload, pod, node
// or team label, over a window. "Could save" is the CPU and memory requested but not
// used -- what right-sizing requests to real use would take off the bill.
const money = (v) => (v >= 1000 ? "$" + (v / 1000).toFixed(1) + "k" : v >= 100 ? "$" + v.toFixed(0) : "$" + v.toFixed(2));
async function showCost(box, fresh) {
  const ctx = S.ctx, ns = S.ns;
  const win = store.get("costWindow") || "1d";
  const agg = store.get("costAgg") || "namespace";
  $("#crumbKind").textContent = "Cost";
  $("#crumbWhere").textContent = `${short(ctx)} · ${ns || "all namespaces"} · loading…`;
  box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Asking OpenCost…" }),
    win === "1d" ? "Usually a few seconds." : "Longer windows take OpenCost longer to add up (7 days: ~10–20 s, 30 days: a minute or more). Kept for ten minutes after."));
  setSpin(true);
  let rep;
  try { rep = await api("/api/cost?" + qs({ context: ctx, namespace: ns, window: win, aggregate: agg, fresh: fresh ? "1" : "" })); }
  catch (e) {
    setSpin(false);
    const oom = /stopped answering|out of memory/i.test(e.message);
    return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: oom ? "OpenCost stopped answering" : "Could not read costs" }),
      oom ? "OpenCost ran out of memory on this question and restarted (its pod shows OOMKilled). Pick a shorter window, or raise OpenCost's memory limit." : e.message,
      el("p", {}, el("button", { type: "button", class: "ghost small", text: "Show the last 24 hours", onclick: () => { store.set("costWindow", "1d"); showCost(box); } }))));
  }
  setSpin(false);
  if (!S.map || S.mapScope !== "cost" || S.ctx !== ctx) return;
  if (rep.none) {
    $("#crumbWhere").textContent = short(ctx);
    return box.replaceChildren(el("div", { class: "ov-pick" }, el("h2", { text: "No cost data in this cluster" }),
      el("p", { class: "muted", text: "K-Vision reads cost from OpenCost (or Kubecost), which this cluster doesn't run. OpenCost is free and open source: it prices every pod from your nodes' cloud prices." }),
      el("pre", { class: "kv-val", text: "helm repo add opencost https://opencost.github.io/opencost-helm-chart\nhelm install opencost opencost/opencost -n opencost --create-namespace" }),
      el("p", { class: "muted", text: "It needs Prometheus. Once it runs, this page fills in by itself." })));
  }
  const seg = (name, value, choices, set) => el("div", { class: "seg", role: "radiogroup", "aria-label": name },
    choices.map(([v, label]) => el("button", { type: "button", role: "radio", "aria-checked": String(value === v), text: label, onclick: () => set(v) })));
  const days = { "1d": 1, "7d": 7, "30d": 30, month: new Date().getDate() }[win] || 1;
  const tile = (label, value, sub, cls) => el("div", { class: "cm-stat" + (cls ? " " + cls : "") },
    el("span", { class: "cm-stat-l", text: label }), el("strong", { text: value }), sub ? el("span", { class: "cm-stat-s", text: sub }) : null);
  const used = rep.total - rep.idle;
  const winLabel = { "1d": "last 24 hours", "7d": "last 7 days", "30d": "last 30 days", month: "this month" }[win] || win;
  $("#crumbWhere").textContent = `${short(ctx)} · ${ns || "all namespaces"} · ${winLabel} · from ${rep.source}` +
    (rep.age > 60 ? ` · ${Math.round(rep.age / 60)} min ago` : "");
  const stats = el("div", { class: "cm-stats" },
    tile(ns ? `Cost of ${ns}` : "Cluster cost", money(rep.total), winLabel),
    tile("Per month", money(rep.monthly), "at this rate"),
    tile("Per day", money(rep.total / days), ""),
    ns ? null : tile("Idle", money(rep.idle), rep.total ? `${Math.round((rep.idle * 100) / rep.total)}% of node cost nobody requested` : "",
      rep.total && rep.idle / rep.total > 0.4 ? "warn" : ""),
    tile("Could save", money(rep.waste), "requested but unused CPU / memory", rep.waste > used * 0.3 ? "warn" : ""));
  const max = Math.max(1e-9, ...rep.rows.map((r) => r.total));
  const eff = (x) => (x ? Math.round(x * 100) + "%" : "—");
  const effCls = (x) => (!x ? "" : x < 0.2 ? "bad" : x < 0.5 ? "warn" : "ok");
  const kindOf = (n) => (/^(\w+):/.exec(n) || [])[1] || "";
  const label = (n) => (n === "__unallocated__" ? "unallocated" : n.replace(/^\w+:/, ""));
  const canDrill = agg === "namespace";
  const rows = rep.rows.map((r) => el("tr", {
      class: canDrill && !r.name.startsWith("__") ? "click" : "",
      title: canDrill ? `Show the workloads of ${r.name}` : "",
      onclick: canDrill && !r.name.startsWith("__") ? () => { store.set("costAgg", "controller"); showMap(r.name, "cost"); } : null },
    el("td", { class: "cost-name" }, kindOf(r.name) ? el("span", { class: "muted", text: kindOf(r.name) + " " }) : null, el("strong", { text: label(r.name) })),
    el("td", { class: "cost-bar" }, el("span", { class: "cb" },
      el("i", { class: "cpu", style: `width:${(r.cpu / max) * 100}%`, title: "CPU " + money(r.cpu) }),
      el("i", { class: "ram", style: `width:${(r.ram / max) * 100}%`, title: "Memory " + money(r.ram) }),
      el("i", { class: "pv", style: `width:${(r.pv / max) * 100}%`, title: "Storage " + money(r.pv) }),
      el("i", { class: "oth", style: `width:${((r.network + r.lb + r.gpu) / max) * 100}%`, title: "Network / load balancers / GPU " + money(r.network + r.lb + r.gpu) }))),
    el("td", { class: "num mono", text: money(r.total) }),
    el("td", { class: "num mono muted", text: money((r.total / days) * 30) }),
    el("td", { class: "num mono " + effCls(r.cpuEfficiency), text: eff(r.cpuEfficiency) }),
    el("td", { class: "num mono " + effCls(r.ramEfficiency), text: eff(r.ramEfficiency) }),
    el("td", { class: "num mono" + (r.waste > r.total * 0.5 && r.waste > 1 ? " warn" : ""), text: r.waste ? money(r.waste) : "—" })));
  const first = { namespace: "NAMESPACE", controller: "WORKLOAD", pod: "POD", node: "NODE", "label:team": "TEAM" }[agg] || agg.toUpperCase();
  const table = el("table", { class: "cost-table" },
    el("thead", {}, el("tr", {}, ...[first, "BREAKDOWN", "COST", "PER MONTH", "CPU USED", "MEMORY USED", "COULD SAVE"].map((h, i) =>
      el("th", { class: i >= 2 ? "num" : "", text: h })))),
    el("tbody", {}, ...rows));
  const legend = el("div", { class: "map-legend" },
    el("span", {}, el("i", { class: "lg-sq cb-cpu" }), "CPU"), el("span", {}, el("i", { class: "lg-sq cb-ram" }), "memory"),
    el("span", {}, el("i", { class: "lg-sq cb-pv" }), "storage"), el("span", {}, el("i", { class: "lg-sq cb-oth" }), "network / LB / GPU"),
    el("span", { class: "muted", text: "Used = average use ÷ request" + (canDrill ? " · click a namespace for its workloads" : "") }));
  const clusters = null;
  box.replaceChildren(...[el("div", { class: "map-bar" },
      seg("Window", win, [["1d", "24h"], ["7d", "7 days"], ["30d", "30 days"], ["month", "This month"]], (v) => { store.set("costWindow", v); showMap(null, "cost"); }),
      seg("Group by", agg, [["namespace", "Namespace"], ["controller", "Workload"], ["pod", "Pod"], ["node", "Node"], ["label:team", "Team"]], (v) => { store.set("costAgg", v); showMap(null, "cost"); }),
      ns ? el("button", { type: "button", class: "ghost small", text: "← All namespaces",
        onclick: () => { S.ns = ""; $("#ns").value = ""; store.set("ns", ""); syncNs(); store.set("costAgg", "namespace"); showMap(null, "cost"); } }) : null,
      el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showCost(box, true) })),
    stats, clusters, legend,
    rep.rows.length ? el("div", { class: "cost-wrap" }, table) : el("div", { class: "empty" }, el("strong", { text: "No cost recorded" }), "OpenCost has nothing for this window yet.")].filter(Boolean));
}

// ------------------------------------------------------------------ warnings dashboard
// Every Warning event of the cluster, grouped by what went wrong: "FailedMount ×42,
// 12 pods, gitlab-runner-spot pool" is one card instead of 42 lines of
// `kubectl get events`. Red = something is broken, amber = slow / waiting.
async function showEvents(box) {
  const ctx = S.ctx, ns = S.ns || "";
  const win = S.evWindow || store.get("evWindow") || "1h";
  $("#crumbKind").textContent = "Warnings";
  $("#crumbWhere").textContent = `${short(ctx)} · ${nsLabel()} · loading…`;
  if (!box.querySelector(".ev-grid")) box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Reading the cluster's events…" })));
  setSpin(true);
  let d;
  try { d = await api("/api/events?" + qs({ context: ctx, namespace: ns, window: win })); }
  catch (e) { setSpin(false); return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not read events" }), e.message)); }
  setSpin(false);
  if (!S.map || S.mapScope !== "events" || S.ctx !== ctx) return;
  $("#crumbWhere").textContent = `${short(ctx)} · ${nsLabel()} · last ${win}`;

  const ago = (t) => { const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
    return s < 60 ? Math.round(s) + "s ago" : s < 3600 ? Math.round(s / 60) + "m ago" : s < 86400 * 2 ? Math.round(s / 3600) + "h ago" : Math.round(s / 86400) + "d ago"; };
  // Open the object in its list. Many warnings are about objects that are already gone
  // (CI job pods, replaced pods): then say so and show what was recorded about it.
  const openObj = async (o, btn) => {
    const k = findKind(o.kind === "Pod" ? "pods" : o.kind);
    if (!k) return toast(`Can't open ${o.kind} ${o.name}`, true);
    try { await api("/api/yaml?" + qs({ context: ctx, kind: kindId(k), namespace: o.namespace || "", name: o.name })); }
    catch (e) {
      if (!/not found/i.test(e.message)) return toast(e.message, true);
      const mine = d.recent.filter((r) => r.kind === o.kind && r.name === o.name && r.namespace === o.namespace);
      const lines = mine.length ? mine.map((r) => `${r.reason} ×${r.count} · ${ago(r.last)}\n${r.message}`).join("\n\n")
        : d.groups.filter((g) => g.examples.some((x) => x.name === o.name && x.namespace === o.namespace)).map((g) => `${g.reason}\n${g.message}`).join("\n\n");
      const where = btn && (btn.closest(".ev-card") || btn.closest(".argo-panel"));
      if (!where) return toast(`${o.kind} ${o.name} no longer exists`);
      for (const old of where.querySelectorAll(".ev-gone")) old.remove();
      where.append(el("div", { class: "ev-gone" },
        el("div", { class: "ev-gone-h" }, el("strong", { text: `${o.kind} ${o.namespace ? o.namespace + "/" : ""}${o.name} no longer exists` }),
          el("span", { class: "muted", text: "deleted after these warnings · what was recorded:" }),
          el("button", { type: "button", class: "ghost small", text: "✕", "aria-label": "Close", onclick: (e) => { e.stopPropagation(); e.target.closest(".ev-gone").remove(); } })),
        el("pre", { class: "mono", text: lines || "(no message kept)" })));
      return;
    }
    if (o.namespace) { S.ns = o.namespace; $("#ns").value = o.namespace; store.set("ns", o.namespace); syncNs(); }
    setKind(kindId(k)).then(() => setFilter(o.name));
  };
  const tile = (label, value, sub, cls) => el("div", { class: "cm-stat" + (cls ? " " + cls : "") },
    el("span", { class: "cm-stat-l", text: label }), el("strong", { text: value }), sub ? el("span", { class: "cm-stat-s", text: sub }) : null);
  const red = d.groups.filter((g) => g.level === "bad");
  const stats = el("div", { class: "cm-stats" },
    tile("Warnings", String(d.warnings), `last ${win}`, d.warnings ? "warn" : "ok"),
    tile("Problems", String(d.groups.length), `${red.length} broken · ${d.groups.length - red.length} slow / waiting`, red.length ? "bad" : ""),
    tile("Objects affected", String(d.objects), ""),
    tile("Top reason", d.byReason[0] ? d.byReason[0].name : "—", d.byReason[0] ? `${d.byReason[0].count} events` : ""),
    tile("Busiest namespace", d.byNamespace[0] ? d.byNamespace[0].name : "—", d.byNamespace[0] ? `${d.byNamespace[0].count} events` : ""),
    tile("Busiest node pool", d.byPool[0] ? d.byPool[0].name : "—", d.byPool[0] ? `${d.byPool[0].count} events` : ""));

  // Warnings over time: one bar per bucket, the latest on the right.
  const max = Math.max(1, ...d.timeline);
  const tl = el("div", { class: "argo-bar ev-tl" },
    el("div", { class: "argo-bar-h" }, el("strong", { text: "Warnings over time" }),
      el("span", { class: "muted", text: ` · ${d.bucketMinutes} min per bar · newest on the right` })),
    el("div", { class: "ev-bars" }, ...d.timeline.map((n, i) => {
      const end = new Date(Date.now() - (d.timeline.length - 1 - i) * d.bucketMinutes * 60000);
      return el("i", { style: `height:${n ? Math.max(6, (n * 100) / max) : 0}%`, title: `${n} warnings · until ${end.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` });
    })));

  const panel = (title, sub, body, cls) => el("section", { class: "argo-panel" + (cls ? " " + cls : "") },
    el("header", {}, el("h3", { text: title }), sub ? el("span", { class: "muted", text: sub }) : null), body);
  const card = (g) => el("div", { class: "ev-card lvl-" + g.level },
    el("div", { class: "ev-card-h" },
      el("span", { class: "pill " + g.level, text: g.reason }),
      el("strong", { text: `×${g.count}` }),
      el("span", { class: "muted", text: `${g.objects} ${g.kind ? g.kind.toLowerCase() : "object"}${g.objects === 1 ? "" : "s"} · last ${ago(g.last)}` }),
      el("span", { class: "grow" }),
      g.pools && g.pools.length ? el("span", { class: "ev-chip", title: "Node pool", text: "⬡ " + g.pools.slice(0, 2).join(", ") + (g.pools.length > 2 ? ` +${g.pools.length - 2}` : "") }) : null,
      el("span", { class: "ev-chip", title: g.namespaces.join(", "), text: g.namespaces.length === 1 ? g.namespaces[0] : `${g.namespaces.length} namespaces` })),
    el("div", { class: "ev-msg mono", text: g.message, title: g.message }),
    g.hint ? el("div", { class: "ev-hint", text: g.hint }) : null,
    el("div", { class: "ev-objs" }, ...g.examples.map((o) => el("button", { type: "button", class: "ev-obj mono", title: `Open ${o.kind} ${o.namespace ? o.namespace + "/" : ""}${o.name}`,
      onclick: (e) => openObj(o, e.currentTarget), text: (o.namespace ? o.namespace + "/" : "") + o.name })),
      g.objects > g.examples.length ? el("span", { class: "muted", text: `+${g.objects - g.examples.length} more` }) : null));
  const problems = panel("Problems", d.groups.length ? `${d.groups.length}, broken first` : "",
    d.groups.length ? el("div", { class: "ev-cards" }, ...d.groups.slice(0, 40).map(card))
      : el("div", { class: "argo-ok" }, el("span", { class: "hm-led argo-led h-ok" }), `No warnings in the last ${win}.`), "ev-problems");

  const countList = (rows, onPick) => el("div", { class: "argo-counts" }, ...rows.slice(0, 10).map((r) =>
    el("button", { type: "button", class: "argo-count", onclick: onPick ? () => onPick(r.name) : null, title: `${r.count} warnings` },
      el("span", { class: "mono", text: r.name }),
      el("span", { class: "cb" }, el("i", { class: "seg-warn", style: `width:${(r.count * 100) / (rows[0].count || 1)}%` })),
      el("strong", { text: String(r.count) }))));
  const recent = panel("Latest", "", el("table", { class: "argo-table" }, el("tbody", {}, ...d.recent.slice(0, 25).map((r) =>
    el("tr", { class: "click", title: `${r.namespace ? r.namespace + "/" : ""}${r.name} · ${r.reason} ×${r.count} · ${ago(r.last)}\n${r.message}`, onclick: (e) => openObj(r, e.currentTarget) },
      el("td", {}, el("span", { class: "hm-led argo-led h-" + r.level }), el("strong", { class: "mono", text: r.name })),
      el("td", {}, el("span", { class: "pill " + r.level, text: r.reason })),
      el("td", { class: "mono muted", text: ago(r.last) }))))));

  const winBtns = el("div", { class: "health" }, ...["15m", "1h", "6h", "24h"].map((w) =>
    el("button", { type: "button", "aria-pressed": String(w === win), text: w,
      onclick: () => { S.evWindow = w; store.set("evWindow", w); showMap(null, "events"); } })));
  box.replaceChildren(
    el("div", { class: "map-bar" }, winBtns,
      el("span", { class: "muted", text: "Warning events, grouped by what went wrong · click a name to open it · Kubernetes keeps events ~1 h by default" }),
      el("span", { class: "grow" }),
      el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showMap(null, "events") })),
    ...(d.errors || []).map((e) => el("div", { class: "err-line", text: e })),
    stats, tl,
    el("div", { class: "ev-grid" }, problems,
      el("div", { class: "ev-side" }, recent,
        panel("By reason", "", countList(d.byReason)),
        panel("By namespace", "", countList(d.byNamespace, (n) => { if (n === "(cluster)") return; S.ns = n; $("#ns").value = n; store.set("ns", n); syncNs(); showMap(null, "events"); })),
        d.byPool.length ? panel("By node pool", "", countList(d.byPool)) : null)));
  // Keep it live while it's open: refresh every 30 s.
  clearTimeout(S.evTimer);
  const tick = () => {
    if (!S.map || S.mapScope !== "events" || S.ctx !== ctx) return;
    // Don't wipe a note the person is reading, or refresh a hidden window.
    if (document.hidden || box.querySelector(".ev-gone")) { S.evTimer = setTimeout(tick, 30000); return; }
    showEvents(box);
  };
  S.evTimer = setTimeout(tick, 30000);
}

// ------------------------------------------------------------------ argo cd dashboard
// The first screen of Argo CD's own UI, from the Application / ApplicationSet /
// AppProject objects: how many apps, their health and sync, what needs a look,
// the latest syncs, and how apps spread over projects, destinations and repos.
async function showArgo(box) {
  const ctx = S.ctx;
  $("#crumbKind").textContent = "Argo CD";
  $("#crumbWhere").textContent = `${short(ctx)} · loading…`;
  box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Reading Argo CD…" })));
  setSpin(true);
  let d;
  try { d = await api("/api/argocd?" + qs({ context: ctx })); }
  catch (e) { setSpin(false); return box.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not read Argo CD" }), e.message)); }
  setSpin(false);
  if (!S.map || S.mapScope !== "argo" || S.ctx !== ctx) return;
  if (!d.installed) {
    $("#crumbWhere").textContent = short(ctx);
    return box.replaceChildren(el("div", { class: "ov-pick" }, el("h2", { text: "Argo CD isn't installed in this cluster" }),
      el("p", { class: "muted", text: "There are no Argo CD Applications here. When Argo CD runs in a cluster, this page shows its apps, their health and sync, and what needs a look." })));
  }
  $("#crumbWhere").textContent = `${short(ctx)} · ${d.apps} applications · ${d.appSets} application sets · ${d.projects} projects`;
  const openKind = (id, filter) => { setKind(id).then(() => { if (filter) setFilter(filter); }); };
  const tile = (label, value, sub, cls, onclick) => el("div", { class: "cm-stat" + (cls ? " " + cls : "") + (onclick ? " click" : ""), onclick },
    el("span", { class: "cm-stat-l", text: label }), el("strong", { text: value }), sub ? el("span", { class: "cm-stat-s", text: sub }) : null);
  const bad = (d.health.Degraded || 0) + (d.health.Missing || 0);
  const oos = d.sync.OutOfSync || 0;
  const stats = el("div", { class: "cm-stats" },
    tile("Applications", String(d.apps), `${d.autoSync} with auto-sync`, "", () => openKind("applications.argoproj.io")),
    tile("Healthy", String(d.health.Healthy || 0), d.apps ? `${Math.round(((d.health.Healthy || 0) * 100) / d.apps)}%` : "", "ok", () => openKind("applications.argoproj.io", "health:healthy")),
    tile("Degraded / missing", String(bad), "", bad ? "bad" : "", bad ? () => openKind("applications.argoproj.io", "health:degraded|missing") : null),
    tile("Out of sync", String(oos), "", oos ? "warn" : "", oos ? () => openKind("applications.argoproj.io", "sync:outofsync") : null),
    tile("Application Sets", String(d.appSets), "", "", () => openKind("applicationsets.argoproj.io")),
    tile("Projects", String(d.projects), "", "", () => openKind("appprojects.argoproj.io")));
  // health and sync as stacked bars
  const bar = (title, parts) => {
    const total = parts.reduce((a, [, n]) => a + n, 0) || 1;
    return el("div", { class: "argo-bar" }, el("div", { class: "argo-bar-h" }, el("strong", { text: title })),
      el("div", { class: "cb argo-cb" }, ...parts.filter(([, n]) => n).map(([k, n, cls]) =>
        el("i", { class: "seg-" + cls, style: `width:${(n * 100) / total}%`, title: `${k}: ${n}` }))),
      el("div", { class: "argo-legend" }, ...parts.filter(([, n]) => n).map(([k, n, cls]) =>
        el("span", {}, el("i", { class: "lg-sq seg-" + cls }), `${k} ${n}`))));
  };
  const H = d.health, Y = d.sync;
  const bars = el("div", { class: "argo-bars" },
    bar("Health", [["Healthy", H.Healthy || 0, "ok"], ["Progressing", H.Progressing || 0, "info"], ["Suspended", H.Suspended || 0, "idle"],
      ["Degraded", H.Degraded || 0, "bad"], ["Missing", H.Missing || 0, "bad2"], ["Unknown", H.Unknown || 0, "idle"]]),
    bar("Sync", [["Synced", Y.Synced || 0, "ok"], ["OutOfSync", Y.OutOfSync || 0, "warn"], ["Unknown", Y.Unknown || 0, "idle"]]));
  const appRow = (a, extra) => el("tr", { class: "click", title: a.message || "",
      onclick: () => { const k = findKind("applications.argoproj.io"); if (!k) return; S.kind = { ...k, id: kindId(k) };
        openPanel({ context: ctx, namespace: a.namespace, name: a.name, status: `${a.health}/${a.sync}`, health: a.level, cells: {}, labels: {} }); } },
    el("td", {}, el("span", { class: "hm-led argo-led h-" + (a.level || "none") }), el("strong", { class: "mono", text: a.name })),
    el("td", {}, el("span", { class: "pill " + (a.health === "Healthy" ? "ok" : a.health === "Degraded" || a.health === "Missing" ? "bad" : "warn"), text: a.health })),
    el("td", {}, el("span", { class: "pill " + (a.sync === "Synced" ? "ok" : "warn"), text: a.sync })),
    el("td", { class: "mono muted", text: extra }));
  const panel = (title, sub, body) => el("section", { class: "argo-panel" }, el("header", {}, el("h3", { text: title }), sub ? el("span", { class: "muted", text: sub }) : null), body);
  const attention = panel("Needs attention", d.attention.length ? `${d.attention.length} apps` : "",
    d.attention.length ? el("table", { class: "argo-table" }, el("tbody", {}, ...d.attention.slice(0, 15).map((a) =>
      appRow(a, a.operation === "Failed" || a.operation === "Error" ? "sync " + a.operation.toLowerCase() + (a.message ? ": " + a.message.slice(0, 60) : "") : a.destination))))
      : el("div", { class: "argo-ok" }, el("span", { class: "hm-led argo-led h-ok" }), "Every application is healthy and in sync."));
  const ago = (t) => { const s = (Date.now() - new Date(t).getTime()) / 1000; return s < 3600 ? Math.round(s / 60) + "m ago" : s < 86400 * 2 ? Math.round(s / 3600) + "h ago" : Math.round(s / 86400) + "d ago"; };
  const recent = panel("Recent syncs", "", el("table", { class: "argo-table" }, el("tbody", {}, ...d.recent.map((a) =>
    appRow(a, `${a.operation || "-"} · ${ago(a.finished)}${a.revision ? " · " + a.revision : ""}`)))));
  const countList = (rows, onPick) => el("div", { class: "argo-counts" }, ...rows.slice(0, 10).map((r) => {
    const max = rows[0] ? rows[0].apps : 1;
    return el("button", { type: "button", class: "argo-count", onclick: onPick ? () => onPick(r.name) : null, title: `${r.apps} apps · ${r.healthy} healthy · ${r.bad} need a look` },
      el("span", { class: "mono", text: r.name }),
      el("span", { class: "cb" }, el("i", { class: "seg-ok", style: `width:${(r.healthy * 100) / max}%` }), el("i", { class: "seg-bad", style: `width:${(r.bad * 100) / max}%` })),
      el("strong", { text: String(r.apps) }));
  }));
  const sets = panel("Application Sets", `${d.appSets}`, el("div", { class: "argo-counts" }, ...d.appSetList.map((s) =>
    el("button", { type: "button", class: "argo-count", onclick: () => { const k = findKind("applicationsets.argoproj.io"); if (!k) return; S.kind = { ...k, id: kindId(k) };
        openPanel({ context: ctx, namespace: s.namespace, name: s.name, status: s.status, health: s.health, cells: {}, labels: {} }); } },
      el("span", { class: "mono" }, el("span", { class: "hm-led argo-led h-" + (s.health || "none") }), s.name),
      el("span", { class: "muted", text: s.status }), el("strong", { text: `${s.apps} app${s.apps === 1 ? "" : "s"}` })))));
  box.replaceChildren(el("div", { class: "map-bar" },
      el("span", { class: "muted", text: "Read from the cluster's Argo CD objects · click anything to open it" }),
      el("button", { type: "button", class: "ghost small", text: "Refresh", onclick: () => showMap(null, "argo") })),
    stats, bars,
    el("div", { class: "argo-grid" }, attention, recent,
      panel("By project", "", countList(d.byProject, (p) => openKind("applications.argoproj.io", "project:" + p))),
      panel("By destination", "", countList(d.byDestination)),
      panel("By repository", "", countList(d.byRepo)),
      sets));
}

// ------------------------------------------------------------------ helm drawer
async function openHelm(row, tab, revision) {
  stopStreams();
  S.panel = null;
  $("#panel").hidden = false;
  $("#panelActions").replaceChildren();
  if (S.helm === "charts") {
    const c = row.helm;
    $("#panelKind").replaceChildren(ctxTag(row.context, true), " Helm chart");
    $("#panelTitle").textContent = c.name;
    $("#panelTabs").replaceChildren();
    $("#panelBody").replaceChildren(el("div", { class: "kv" },
      c.description ? el("p", { class: "muted", text: c.description }) : null,
      el("div", { class: "kv-row" }, el("div", { class: "kv-head" }, el("code", { class: "kv-key", text: "Versions deployed here" })),
        el("pre", { class: "kv-val", text: c.versions.join("\n") })),
      el("div", { class: "kv-row" }, el("div", { class: "kv-head" }, el("code", { class: "kv-key", text: `Releases using it (${c.releases.length})` })),
        el("div", { class: "kv-val" }, c.releases.map((r) => el("div", {}, el("a", { href: "#", text: r, onclick: (e) => {
          e.preventDefault(); const [ns, name] = r.split("/"); showHelm("releases").then(() => {
            const hit = S.rows.find((x) => x.namespace === ns && x.name === name); if (hit) openHelm(hit); });
        } }))))),
      c.versions.length > 1 ? el("p", { class: "muted", text: "More than one version of this chart is deployed in this cluster." }) : null));
    return;
  }
  const r = row.helm;
  tab = tab || "values";
  $("#panelKind").replaceChildren(ctxTag(row.context, true), ` Helm release · ${r.namespace}`);
  $("#panelTitle").textContent = r.name;
  const tabs = [["values", "Values"], ["all", "All values"], ["manifest", "Manifest"], ["notes", "Notes"], ["history", "History"]];
  $("#panelTabs").replaceChildren(...tabs.map(([id, label]) => el("button", { role: "tab", type: "button",
    "aria-selected": String(tab === id), text: label, onclick: () => openHelm(row, id, revision) })));
  const body = $("#panelBody");
  body.replaceChildren(el("div", { class: "empty", text: "Loading…" }));
  let d;
  try { d = await api("/api/helm/release?" + qs({ context: row.context, namespace: r.namespace, name: r.name, revision: revision || 0 })); }
  catch (e) { return body.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not load" }), e.message)); }
  const head = el("div", { class: "helm-head" },
    el("span", { class: "pill " + (d.health || ""), text: d.status }),
    el("span", { class: "mono", text: `${d.chart}-${d.chartVersion}` }),
    d.appVersion ? el("span", { class: "muted", text: `app ${d.appVersion}` }) : null,
    el("span", { class: "muted", text: `revision ${d.revision}${revision ? " (older)" : ""} · ${ageOf(d.updated)} ago` }),
    d.chartSource ? el("a", { href: d.chartSource, target: "_blank", rel: "noopener", text: "chart source" }) : null);
  const text = (t, empty) => t ? yamlPre(t) : el("div", { class: "empty", text: empty });
  let view;
  if (tab === "values") view = text(d.values, "No values were set for this release; it uses the chart's defaults (see All values).");
  else if (tab === "all") view = text(d.allValues, "The chart has no values.");
  else if (tab === "manifest") view = text(d.manifest.length > 400000 ? d.manifest.slice(0, 400000) + "\n# … cut at 400 KB" : d.manifest, "Empty manifest.");
  else if (tab === "notes") view = d.notes ? el("pre", { text: d.notes }) : el("div", { class: "empty", text: "This chart has no notes." });
  else view = el("table", { class: "hist" }, el("thead", {}, el("tr", {}, ["REV", "STATUS", "CHART", "APP", "UPDATED", "DESCRIPTION"].map((h) => el("th", { text: h })))),
    el("tbody", {}, d.history.map((h) => el("tr", { class: h.revision === d.revision ? "sel" : "", onclick: () => openHelm(row, "values", h.revision) },
      el("td", { class: "mono", text: String(h.revision) }), el("td", {}, el("span", { class: "pill " + (h.health || ""), text: h.status })),
      el("td", { class: "mono", text: h.chartVersion }), el("td", { class: "mono", text: h.appVersion || "-" }),
      el("td", { class: "mono", text: ageOf(h.updated) + " ago" }), el("td", { text: h.description || "" })))));
  const copyBtn = tab !== "history" ? el("button", { type: "button", class: "ghost small", text: "Copy",
    onclick: () => copy(tab === "values" ? d.values : tab === "all" ? d.allValues : tab === "manifest" ? d.manifest : d.notes) }) : null;
  body.replaceChildren(el("div", { class: "pane" }, el("div", { class: "tools" }, head, el("span", { class: "grow" }), copyBtn), view));
}

// ------------------------------------------------------------------ splitters
// Drag a divider to resize; the size is remembered; double-click restores the
// default. Pointer capture keeps the drag going over the terminal and the table.
function splitter(handle, { prop, axis, min, max, def, sign = 1, key, onResize }) {
  const root = document.querySelector("#app");
  const set = (px) => {
    const v = Math.round(Math.max(min(), Math.min(max(), px)));
    root.style.setProperty(prop, v + "px");
    if (onResize) onResize();
    return v;
  };
  const saved = parseInt(store.get(key), 10);
  if (saved) set(saved);
  let start = 0, startPx = 0, last = 0;
  handle.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    start = axis === "x" ? e.clientX : e.clientY;
    startPx = parseFloat(getComputedStyle(root).getPropertyValue(prop)) || handle.parentElement.getBoundingClientRect()[axis === "x" ? "width" : "height"];
    if (prop === "--panel-w" || prop === "--dock-h") startPx = handle.parentElement.getBoundingClientRect()[axis === "x" ? "width" : "height"];
    handle.setPointerCapture(e.pointerId);
    try { getSelection().removeAllRanges(); } catch (_) {}
    handle.classList.add("drag");
    document.body.classList.add("resizing", axis === "x" ? "col" : "row");
  });
  handle.addEventListener("pointermove", (e) => {
    if (!handle.hasPointerCapture(e.pointerId)) return;
    const d = (axis === "x" ? e.clientX : e.clientY) - start;
    last = set(startPx + sign * d);
  });
  const end = (e) => {
    if (!handle.hasPointerCapture(e.pointerId)) return;
    handle.releasePointerCapture(e.pointerId);
    handle.classList.remove("drag");
    document.body.classList.remove("resizing", "col", "row");
    if (last) store.set(key, String(last));
  };
  handle.addEventListener("pointerup", end);
  handle.addEventListener("pointercancel", end);
  handle.addEventListener("dblclick", () => { root.style.removeProperty(prop); store.set(key, null); if (onResize) onResize(); });
}

function wireSplitters() {
  splitter($("#sideSplit"), { prop: "--side-w", axis: "x", key: "sideW", def: 248,
    min: () => 190, max: () => Math.min(480, innerWidth * 0.4) });
  splitter($("#panelSplit"), { prop: "--panel-w", axis: "x", sign: -1, key: "panelW",
    min: () => 360, max: () => innerWidth - $("#side").getBoundingClientRect().width - 320 });
  splitter($("#dockResize"), { prop: "--dock-h", axis: "y", sign: -1, key: "dockH2",
    min: () => 120, max: () => innerHeight - 140, onResize: () => window.dispatchEvent(new Event("kc-dock-resized")) });
}

// ------------------------------------------------------------------ welcome
// The first screen: pick a cluster, then the workspace opens on it. Each card is
// checked in the background (reachable, Kubernetes version, nodes), so an expired SSO
// session or a VPN-only endpoint shows before you open it, not after.
const probes = new Map();
let wlSel = 0;

function recentList() {
  try { return JSON.parse(store.get("recent") || "[]").filter((n) => S.contexts.some((c) => c.name === n)); } catch (_) { return []; }
}
function rememberRecent(name) {
  const r = [name, ...recentList().filter((n) => n !== name)].slice(0, 4);
  store.set("recent", JSON.stringify(r));
}

function showWelcome() {
  closePanel();
  $("#welcome").hidden = false;
  $("#app").classList.add("behind");
  try { KVHero.start($("#wlArt")); } catch (_) {}
  const n = S.contexts.length;
  const prot = S.contexts.filter((c) => c.protected).length;
  $("#wlSub").textContent = `${n} cluster${n === 1 ? "" : "s"} in your kubeconfig` +
    (S.hiddenCtx.length ? ` · ${S.hiddenCtx.length} removed from the list` : "");
  $("#wlFind").value = "";
  S.wlChoice = null; S.wlTyped = false; $("#wlGo").disabled = true; $("#wlChosen").hidden = true; $("#wlChosen").dataset.done = "";
  $("#wlComboLed").className = "wl-combo-led";
  openDrop(false);
  renderWelcome();
  const last = recentList()[0] || (S.contexts.find((c) => c.current) || {}).name;
  if (last && S.contexts.some((c) => c.name === last)) chooseCluster(last);
  setTimeout(() => (S.wlChoice ? $("#wlGo") : $("#wlFind")).focus(), 0);
  for (const c of S.contexts) if (!probes.has(c.name)) probeOne(c.name);
}

function hideWelcome() {
  $("#welcome").hidden = true;
  $("#app").classList.remove("behind");
  try { KVHero.stop(); } catch (_) {}
}

async function probeOne(name) {
  probes.set(name, { pending: true });
  renderWelcome();
  try { probes.set(name, await api("/api/probe?" + qs({ context: name }))); }
  catch (e) { probes.set(name, { ok: false, error: e.message }); }
  renderWelcome();
}

function wlCard(c, i, recent) {
  const p = probes.get(c.name) || { pending: true };
  const reason = (e) => /no such host|i\/o timeout|dial tcp/i.test(e) ? "Can't reach the API — VPN or private endpoint?"
    : /expired|sso|token/i.test(e) ? "Login expired — run aws sso login"
    : /Unauthorized|forbidden/i.test(e) ? "Not authorized" : (e || "").split(":").pop().trim().slice(0, 60);
  const state = p.pending ? el("span", { class: "wl-state", text: "Checking…" })
    : p.ok ? el("span", { class: "wl-state ok" }, el("span", { class: "wl-led ok" }), `${p.version || "?"} · ${p.nodes} node${p.nodes === 1 ? "" : "s"}`)
    : el("span", { class: "wl-state bad", title: p.error }, el("span", { class: "wl-led bad" }), reason(p.error));
  return el("button", {
      type: "button", role: "option", class: "wl-row" + (i === wlSel ? " on" : "") + (p.ok === false && !p.pending ? " down" : "") + (S.wlChoice === c.name ? " chosen" : ""),
      style: `--h:${ctxHue(c.name)}`, title: c.name, "data-i": i, "aria-selected": String(S.wlChoice === c.name),
      onmousedown: (e) => e.preventDefault(), // keep focus in the input
      onclick: () => chooseCluster(c.name),
      ondblclick: () => enterCluster(c.name),
      oncontextmenu: (e) => clusterMenu(e, c.name),
      onmousemove: () => { if (wlSel !== i) { wlSel = i; markWl(); } },
    },
    el("span", { class: "wl-stripe" }),
    el("span", { class: "wl-name", text: short(c.name) }),
    state,
    c.current ? el("span", { class: "wl-cur", text: "kubectl's current" }) : recent ? el("span", { class: "wl-recent", text: "recent" }) : el("span"));
}

// The start screen is a picker: choose a cluster (from the list, or by typing), then
// Continue. Double-click or Enter on a row goes straight in.
function chooseCluster(name) {
  S.wlChoice = name;
  S.wlTyped = false;
  const c = S.contexts.find((x) => x.name === name);
  const p = probes.get(name) || { pending: true };
  $("#wlFind").value = short(name);
  $("#wlGo").disabled = false;
  $("#wlComboLed").className = "wl-combo-led " + (p.ok ? "ok" : p.pending ? "" : "bad");
  $("#wlComboLed").style.setProperty("--h", ctxHue(name));
  const ch = $("#wlChosen");
  ch.hidden = false;
  ch.replaceChildren(
    p.ok ? el("span", {}, el("span", { class: "wl-led ok" }), `Kubernetes ${p.version || "?"} · ${p.nodes} node${p.nodes === 1 ? "" : "s"}`)
      : p.pending ? el("span", { class: "muted", text: "Checking the cluster…" })
      : el("span", { class: "bad-text" }, el("span", { class: "wl-led bad" }), (p.error || "").split(":").pop().trim().slice(0, 90)),
    el("span", { class: "wl-arn", text: c && c.name !== short(c.name) ? c.name : (c && c.cluster) || "" }));
  openDrop(false);
  setTimeout(() => $("#wlGo").focus(), 0);
}
function openDrop(on) {
  $("#wlDrop").hidden = !on;
  $("#wlFind").setAttribute("aria-expanded", String(on));
  $("#wlCombo").classList.toggle("open", on);
  if (on) renderWelcome();
}

// What filters the list: what was typed. The chosen cluster's name in the box (set by
// chooseCluster) is not a search -- opening the list then shows every cluster.
const wlQuery = () => (S.wlTyped ? ($("#wlFind").value || "").trim() : "");
function wlItems() {
  const q = wlQuery().toLowerCase();
  return S.contexts.filter((c) => !q || c.name.toLowerCase().includes(q));
}

function renderWelcome() {
  if ($("#welcome").hidden) return;
  const q = wlQuery();
  const items = wlItems();
  const recent = q ? [] : recentList().map((n) => S.contexts.find((c) => c.name === n)).filter(Boolean);
  const rest = q ? items : items.filter((c) => !recent.includes(c));
  const order = [...recent, ...rest];
  if (wlSel >= order.length) wlSel = Math.max(0, order.length - 1);
  // One list: clusters used recently first (tagged), then the rest. No section headings.
  $("#wlRecent").hidden = true;
  $("#wlRecentList").replaceChildren();
  $("#wlAllTitle").hidden = !q;
  $("#wlAllTitle").textContent = `${items.length} match${items.length === 1 ? "" : "es"}`;
  $("#wlList").replaceChildren(...recent.map((c, i) => wlCard(c, i, true)), ...rest.map((c, i) => wlCard(c, recent.length + i)));
  if (!order.length) $("#wlList").append(el("div", { class: "wl-none", text: "No cluster matches. Clear the search, or check Manage clusters." }));
  S.wlOrder = order;
  if (S.wlChoice && !$("#wlChosen").hidden) chooseClusterRefresh();
}

function markWl() {
  for (const b of document.querySelectorAll("#welcome .wl-row")) b.classList.toggle("on", b.dataset.i === String(wlSel));
  const on = document.querySelector("#welcome .wl-row.on");
  if (on) on.scrollIntoView({ block: "nearest" });
}
// The chosen cluster's line updates when its health check comes back.
function chooseClusterRefresh() {
  const name = S.wlChoice, p = probes.get(name);
  if (!p || p.pending || $("#wlChosen").dataset.done === name) return;
  $("#wlChosen").dataset.done = name;
  const v = $("#wlFind").value;
  chooseCluster(name);
  $("#wlFind").value = v;
}

async function enterCluster(name, every) {
  hideWelcome();
  rememberRecent(name);
  await switchContext(name, store.get("kind") || "pods");
  if (every) setEverywhere(true);
}

function wireWelcome() {
  $("#wlFind").oninput = () => {
    wlSel = 0; S.wlChoice = null; S.wlTyped = true; $("#wlGo").disabled = true; $("#wlChosen").hidden = true; $("#wlComboLed").className = "wl-combo-led";
    openDrop(true);
  };
  const openFull = () => {
    if (S.wlChoice && !S.wlTyped) { $("#wlFind").select(); const i = (S.wlOrder || []).findIndex((c) => c.name === S.wlChoice); if (i >= 0) wlSel = i; }
    openDrop(true); markWl();
  };
  $("#wlFind").onfocus = openFull;
  $("#wlFind").onclick = openFull;
  $("#wlFind").onblur = () => setTimeout(() => { if (document.activeElement !== $("#wlFind")) openDrop(false); }, 120);
  $("#wlComboBtn").onmousedown = (e) => { e.preventDefault(); const open = $("#wlDrop").hidden; openDrop(open); if (open) $("#wlFind").focus(); };
  $("#wlFind").onkeydown = (e) => {
    const n = (S.wlOrder || []).length;
    if (e.key === "ArrowDown") { if ($("#wlDrop").hidden) openDrop(true); else { wlSel = Math.min(n - 1, wlSel + 1); markWl(); } e.preventDefault(); }
    else if (e.key === "ArrowUp") { wlSel = Math.max(0, wlSel - 1); markWl(); e.preventDefault(); }
    else if (e.key === "Escape") { openDrop(false); }
    else if (e.key === "Enter") {
      e.preventDefault();
      if (!$("#wlDrop").hidden && S.wlOrder && S.wlOrder[wlSel]) chooseCluster(S.wlOrder[wlSel].name);
      else if (S.wlChoice) enterCluster(S.wlChoice);
    }
  };
  $("#wlGo").onclick = () => { if (S.wlChoice) enterCluster(S.wlChoice); };
  $("#wlEvery").onclick = () => enterCluster((S.contexts.find((c) => c.current) || S.contexts[0]).name, true);
  $("#wlManage").onclick = () => { hideWelcome(); showClusters(); };
  $("#brandHome").onclick = showWelcome;
}

// ------------------------------------------------------------------ cluster list
// Hiding a cluster takes it out of K-Vision's lists only; the kubeconfig and
// kubectl are untouched, and "Show" brings it back. Right-click a cluster (in the
// Clusters panel or on a cluster chip) for the menu.
function fillContextSelect() {
  const sel = $("#ctx");
  sel.replaceChildren(...S.contexts.map((c) => el("option", { value: c.name, text: short(c.name) })));
  sel.value = S.ctx;
  const n = S.hiddenCtx.length;
  $("#btnClusters").textContent = n ? `Manage clusters · ${n} removed` : "Manage clusters";
  renderCtxList();
}

// The sidebar cluster list. Click to open; hover shows ✕ to remove it from the list;
// right-click for the menu. "Remove from list" hides it in K-Vision only.
function renderCtxList() {
  const cur = $("#secClustersCur");
  if (cur) {
    cur.textContent = S.everywhere ? "All clusters" : short(S.ctx);
    cur.title = S.everywhere ? "Every cluster at once" : S.ctx;
    cur.style.setProperty("--h", S.everywhere ? 212 : ctxHue(S.ctx));
  }
  const q = ($("#ctxFind").value || "").toLowerCase();
  const items = S.contexts.filter((c) => !q || c.name.toLowerCase().includes(q));
  $("#ctxFind").hidden = S.contexts.length < 8;
  $("#ctxList").replaceChildren(...items.map((c) => el("li", {
      class: "ctx-item" + (c.name === S.ctx && !S.everywhere ? " on" : ""), role: "option",
      "aria-selected": String(c.name === S.ctx), title: c.name,
      onclick: () => { if (S.everywhere) setEverywhere(false); if (c.name !== S.ctx) { rememberRecent(c.name); switchContext(c.name); } },
      oncontextmenu: (e) => clusterMenu(e, c.name),
    },
    el("span", { class: "ctx-dot" + (c.protected ? " prot" : "") }),
    el("span", { class: "ctx-name", text: short(c.name) }),
    c.current ? el("span", { class: "ctx-cur", text: "kubectl", title: "kubectl's current context" }) : null,
    !c.current ? el("button", { type: "button", class: "ctx-x", title: "Remove from list (kubectl keeps it)", "aria-label": `Remove ${short(c.name)} from the list`,
      onclick: (e) => { e.stopPropagation(); setHidden(c.name, true); } }, "✕") : null)));
  if (!items.length) $("#ctxList").append(el("li", { class: "ctx-empty muted", text: "No cluster matches" }));
}

async function setHidden(name, hide) {
  try {
    const r = await post("/api/context/hide", { context: name, hide });
    S.contexts = r.contexts; S.hiddenCtx = r.hidden || []; try { Tabs.prune(S.contexts); } catch (_) {}
    fillContextSelect();
    renderWelcome();
    toast(hide ? `Removed ${short(name)} from the list. kubectl still has it; "Show removed clusters" brings it back.` : `${short(name)} is back in the list`);
    if (hide && S.ctx === name) await switchContext((S.contexts.find((c) => c.current) || S.contexts[0]).name);
    else if (S.everywhere) load();
    if (S.panel == null && !$("#panel").hidden && $("#panelTitle").textContent === "Clusters") showClusters();
  } catch (e) { toast(e.message, true); }
}

let menuEl = null;
function closeMenu() { if (menuEl) { menuEl.remove(); menuEl = null; } }
function clusterMenu(e, name) {
  e.preventDefault();
  closeMenu();
  const hidden = S.hiddenCtx.some((c) => c.name === name);
  const isCurrent = (S.contexts.find((c) => c.name === name) || {}).current;
  const item = (label, fn, disabled, hint) => el("button", { type: "button", class: "menu-item", disabled,
    title: hint || "", onclick: () => { closeMenu(); fn(); } }, label);
  menuEl = el("div", { class: "menu", role: "menu" },
    el("div", { class: "menu-head", text: short(name) }),
    !hidden ? item("Open", () => switchContext(name)) : null,
    hidden ? item("Put back in the list", () => setHidden(name, false))
      : item("Remove from list", () => setHidden(name, true), isCurrent,
        isCurrent ? "This is kubectl's current context, so it stays in the list." : ""),
    item("Copy context name", () => copy(name)),
    el("div", { class: "menu-sep" }),
    item("Delete from kubeconfig…", () => deleteFromKubeconfig(name), isCurrent,
      isCurrent ? "kubectl's current context can't be deleted; switch kubectl to another one first." : ""));
  menuEl.lastChild.classList.add("danger-item");
  document.body.append(menuEl);
  const r = menuEl.getBoundingClientRect();
  menuEl.style.left = Math.min(e.clientX, innerWidth - r.width - 8) + "px";
  menuEl.style.top = Math.min(e.clientY, innerHeight - r.height - 8) + "px";
  setTimeout(() => document.addEventListener("pointerdown", (ev) => { if (!menuEl || !menuEl.contains(ev.target)) closeMenu(); }, { once: true }), 0);
}

// Delete a context from the kubeconfig file, as Lens does: kubectl loses it too.
// The server backs the file up first and needs the cluster's name typed.
async function deleteFromKubeconfig(name) {
  const want = short(name);
  const confirm = await ask({
    title: `Delete ${want} from your kubeconfig?`,
    text: `This removes it from ~/.kube/config, so kubectl and every other tool lose it too — not just K-Vision. ` +
      `Its cluster and credentials are removed as well unless another context still uses them.\n\n` +
      `The file is backed up to ~/.k-vision/kubeconfig-backups first. ` +
      `To only take it out of K-Vision's list, use "Remove from list" instead.`,
    want, ok: "Delete from kubeconfig", danger: true,
  });
  if (confirm == null) return;
  try {
    const r = await post("/api/context/delete", { context: name, confirm });
    S.contexts = r.contexts; S.hiddenCtx = r.hidden || []; try { Tabs.prune(S.contexts); } catch (_) {}
    fillContextSelect();
    toast(r.ok);
    if (S.ctx === name) await switchContext((S.contexts.find((c) => c.current) || S.contexts[0]).name);
    if (!$("#panel").hidden && $("#panelTitle").textContent === "Clusters") showClusters();
  } catch (e) { toast(e.message, true); }
}

function showClusters() {
  stopStreams();
  S.panel = null;
  $("#panel").hidden = false;
  $("#panelKind").textContent = "Kubeconfig";
  $("#panelTitle").textContent = "Clusters";
  $("#panelTabs").replaceChildren();
  $("#panelActions").replaceChildren();
  const row = (c, hidden) => el("li", { oncontextmenu: (e) => clusterMenu(e, c.name), class: hidden ? "is-hidden" : "" },
    ctxTag(c.name, true),
    el("span", { class: "grow muted mono", text: c.name, title: c.name }),
    c.current ? el("span", { class: "muted", text: "current" }) : null,
    hidden ? el("button", { type: "button", text: "Put back", onclick: () => setHidden(c.name, false) })
      : el("button", { type: "button", class: "ghost", text: "Remove", disabled: c.current,
        title: c.current ? "kubectl's current context stays in the list" : "Remove from K-Vision's list", onclick: () => setHidden(c.name, true) }),
    el("button", { type: "button", class: "ghost small danger-text", text: "Delete…", disabled: c.current,
      title: c.current ? "kubectl's current context can't be deleted" : "Delete from your kubeconfig file", onclick: () => deleteFromKubeconfig(c.name) }));
  $("#panelBody").replaceChildren(
    el("div", { class: "explain" },
      el("div", {}, el("strong", { text: "Remove" }), " takes a cluster out of K-Vision's lists only. Your kubeconfig isn't changed and kubectl keeps it. Put back restores it."),
      el("div", {}, el("strong", { text: "Delete…" }), " deletes it from your kubeconfig file, like kubectl config delete-context. kubectl loses it too. The file is backed up first.")),
    el("ul", { class: "list" }, S.contexts.map((c) => row(c, false))),
    S.hiddenCtx.length ? el("div", { class: "grp-title", text: `Removed (${S.hiddenCtx.length})` }) : null,
    S.hiddenCtx.length ? el("ul", { class: "list" }, S.hiddenCtx.map((c) => row(c, true))) : null);
}

async function switchContext(name, kid, ns) {
  // A namespace belongs to a cluster: a different cluster starts on All namespaces.
  // (Re-opening the same cluster -- start screen, refresh -- keeps the one chosen;
  // a tab passes the namespace it was on.)
  S.nsNone = false;
  if (ns != null) { S.ns = ns; store.set("ns", ns); }
  else if (name !== S.ctx) { S.ns = ""; store.set("ns", ""); syncNs(); $("#ns").value = ""; syncNs(); }
  S.ctx = name; S.everywhere = false; store.set("ctx", name);
  $("#ctx").value = name;
  renderCtxList();
  $("#btnEverywhere").classList.remove("on");
  $("#everyLabel").textContent = "All clusters";
  closePanel();
  setSpin(true);
  try {
    S.kinds = await api("/api/kinds?" + qs({ context: name }));
  } catch (e) {
    setSpin(false); S.rows = []; S.errors = [`${short(name)}: ${e.message}`]; render();
    return;
  }
  buildNav();
  loadNamespaces();
  await setKind(kid || (S.kind && S.kind.id) || "pods");
}

async function loadNamespaces() {
  S.nsList = [];
  try {
    S.nsList = await api("/api/namespaces?" + qs({ context: S.ctx }));
    // keep only chosen namespaces that exist in this cluster
    const keep = nsChosen().filter((n) => S.nsList.includes(n));
    if (keep.join(",") !== S.ns) { S.ns = keep.join(","); store.set("ns", S.ns); }
  } catch (_) { /* RBAC may forbid listing namespaces; All still works */ }
  syncNs();
}

// ------------------------------------------------------------------ namespace picker
// One, several or all namespaces. S.ns is "" (all), "a" or "a,b,c"; the server lists
// each and merges them. The hidden <select id="ns"> mirrors S.ns for older code paths.
const nsChosen = () => (S.ns ? S.ns.split(",").filter(Boolean) : []);
const nsLabel = () => { const c = nsChosen(); return S.nsNone ? "no namespace" : !c.length ? "all namespaces" : c.length <= 3 ? c.join(", ") : `${c.length} namespaces`; };
function syncNs() {
  const sel = $("#ns");
  sel.replaceChildren(el("option", { value: "", text: "All namespaces" }), ...(S.ns ? [el("option", { value: S.ns, text: S.ns })] : []));
  sel.value = S.ns;
  const ch = nsChosen();
  // The box reads like a closed dropdown: what is chosen, as its placeholder; typing filters.
  const f = $("#nsFind");
  f.placeholder = S.nsNone ? "No namespace selected" : !ch.length ? "All namespaces" : ch.length === 1 ? ch[0] : ch.length <= 2 ? ch.join(", ") : `${ch.length} namespaces`;
  $("#nsBtn").title = ch.length ? ch.join(", ") : "All namespaces";
  $("#nsBtn").classList.toggle("some", ch.length > 0 || !!S.nsNone);
  if (!$("#nsPop").hidden) renderNsList();
}
// The list: "All namespaces" first, then every namespace, each with a checkbox. With
// nothing narrowed, everything is ticked (that is what "all" means); unticking one
// leaves the rest ticked; ticking "All namespaces" goes back to all.
function renderNsList() {
  const q = ($("#nsFind").value || "").toLowerCase().trim();
  const all = S.nsList || [];
  const ch = nsChosen();
  const none = !!S.nsNone;
  const isAll = !none && ch.length === 0;
  const on = (n) => !none && (isAll || ch.includes(n));
  const items = all.filter((n) => !q || n.toLowerCase().includes(q));
  const row = (label, checked, onToggle, cls, title) => el("label", { class: "ns-item" + (cls ? " " + cls : "") + (checked ? " on" : ""), role: "option", "aria-selected": String(checked), title: title || label },
    el("input", { type: "checkbox", checked, onchange: (e) => onToggle(e.target.checked) }),
    el("span", { text: label }));
  const out = [];
  if (!q) out.push(row("All namespaces", isAll, (v) => (v ? setNamespaces([]) : setNoNamespaces()), "ns-all-row",
    isAll ? "Showing every namespace. Untick one below to leave it out." : "Show every namespace"));
  for (const n of items) {
    const r = row(n, on(n), (v) => {
      let next = none ? [] : isAll ? all.slice() : ch.slice();
      next = v ? [...new Set([...next, n])] : next.filter((x) => x !== n);
      // every one ticked again means all; none ticked is allowed while you pick
      if (!next.length) return setNoNamespaces();
      setNamespaces(all.length && next.length === all.length ? [] : next);
    });
    r.append(el("button", { type: "button", class: "ns-only", text: "only", title: `Only ${n}`,
      onclick: (e) => { e.preventDefault(); e.stopPropagation(); setNamespaces([n]); closeNsPop(); } }));
    out.push(r);
  }
  if (!items.length) out.push(el("div", { class: "muted ns-none", text: all.length ? "No namespace matches" : "Namespaces can't be listed here; type one and press Enter" }));
  $("#nsList").replaceChildren(...out);
}
function toggleNs(n, on) {
  const all = S.nsList || [];
  let ch = nsChosen().length ? nsChosen() : all.slice();
  ch = on ? [...new Set([...ch, n])] : ch.filter((x) => x !== n);
  setNamespaces(ch.length === all.length || !ch.length ? [] : ch);
}
// Apply a namespace choice and reload whatever is on screen.
function setNamespaces(list) {
  S.nsNone = false;
  S.ns = [...new Set(list)].join(",");
  store.set("ns", S.ns);
  syncNs();
  nsChanged();
}
// Everything unticked: an in-between state while picking (nothing is listed). Closing
// the list with nothing ticked goes back to all namespaces.
function setNoNamespaces() {
  S.nsNone = true;
  syncNs();
  if (!S.map && !S.overview && S.kind && !S.empty) { clearTimeout(S.timer); S.seq++; setSpin(false); S.rows = []; S.errors = []; render(); }
}
function nsChanged() {
  if (S.map) showMap(null, ["helm", "cost", "argo", "events"].includes(S.mapScope) ? S.mapScope : S.ns ? "namespace" : S.mapScope);
  else if (S.overview) showOverview();
  else load();
}
function openNsPop() {
  if (!$("#nsPop").hidden) return;
  $("#nsPop").hidden = false; $("#nsBtn").setAttribute("aria-expanded", "true"); $("#nsBtn").classList.add("open");
  renderNsList();
}
function closeNsPop() {
  if (S.nsNone) setNamespaces([]);
  $("#nsPop").hidden = true; $("#nsBtn").setAttribute("aria-expanded", "false"); $("#nsBtn").classList.remove("open");
  $("#nsFind").value = ""; $("#nsFind").blur();
}
function wireNsPicker() {
  const f = $("#nsFind");
  f.onfocus = openNsPop;
  f.onclick = (e) => { e.stopPropagation(); openNsPop(); };
  $("#nsCaret").onclick = (e) => { e.stopPropagation(); $("#nsPop").hidden ? (f.focus(), openNsPop()) : closeNsPop(); };
  $("#nsBtn").onclick = (e) => { e.stopPropagation(); f.focus(); };
  $("#nsPop").onclick = (e) => e.stopPropagation();
  $("#nsPop").onmousedown = (e) => { if (e.target !== f) e.preventDefault(); }; // keep focus in the box
  document.addEventListener("click", () => { if (!$("#nsPop").hidden) closeNsPop(); });
  f.oninput = renderNsList;
  f.onkeydown = (e) => {
    if (e.key === "Escape") { e.stopPropagation(); closeNsPop(); }
    if (e.key === "Enter") {
      const v = f.value.trim();
      if (!v) return closeNsPop();
      const list = S.nsList || [];
      const n = list.includes(v) ? v : list.find((x) => x.toLowerCase().includes(v.toLowerCase())) || v;
      setNamespaces([n]); closeNsPop();
    }
  };
}

function findKind(q) {
  q = (q || "").toLowerCase();
  return S.kinds.find((k) => kindId(k) === q) ||
    S.kinds.find((k) => k.name === q || (k.kind || "").toLowerCase() === q || (k.shortNames || []).includes(q)) ||
    S.kinds.find((k) => k.name.replace(/s$/, "") === q.replace(/s$/, ""));
}

function buildNav() {
  const nav = $("#nav");
  nav.replaceChildren();
  const served = new Set(S.kinds.map(kindId));
  const current = S.kind ? S.kind.id : store.get("kind") || "pods";
  const link = (id, label) => el("a", { "data-kind": id, onclick: () => setKind(id) },
    el("span", { class: "nav-label", text: label }), el("span", { class: "n" }));
  const section = (name, icon, links, extra) => {
    const holdsCurrent = links.some((l) => l.dataset.kind === current || (S.map && l.dataset.kind === "map:" + S.mapScope));
    const open = sectionOpen(name, name === "Workloads" || holdsCurrent);
    return el("div", { class: "sec" + (open ? " open" : ""), "data-sec": name },
      sectionHead(name, icon, null, open), el("div", { class: "sec-body" }, ...links, extra || null));
  };
  // Topology first, then Cost, then the resource sections.
  nav.append(section("Topology", "⌗", [
    el("a", { "data-kind": "map:cluster", class: "nav-overview", onclick: () => showMap(null, "cluster") },
      el("span", { class: "nav-label", text: "Cluster Topology" }), el("span", { class: "n" })),
    el("a", { "data-kind": "map:namespace", class: "nav-overview", onclick: () => showMap(null, "namespace") },
      el("span", { class: "nav-label", text: "Namespace Topology" }), el("span", { class: "n" })),
    el("a", { "data-kind": "map:helm", class: "nav-overview", onclick: () => showMap(null, "helm") },
      el("span", { class: "nav-label", text: "Helm Topology" }), el("span", { class: "n" })),
  ]));
  nav.append(section("Cost", "$", [
    el("a", { "data-kind": "map:cost", class: "nav-overview", onclick: () => showMap(null, "cost") },
      el("span", { class: "nav-label", text: "Overview" }), el("span", { class: "n" })),
  ]));
  for (const [name, icon, items] of NAV) {
    const links = items.filter(([id]) => id === "overview" || id === "map" || id === "portforwards" || served.has(id) || (id === "argo" && served.has("applications.argoproj.io")) || (id === "warnings" && served.has("events"))).map(([id, label]) =>
      id === "portforwards" ? el("a", { "data-kind": "portforwards", class: "nav-overview", onclick: showForwards },
        el("span", { class: "nav-label", text: label }), el("span", { id: "pfCount", class: "count-pill", hidden: true }))
      : id === "argo" ? el("a", { "data-kind": "map:argo", class: "nav-overview", onclick: () => showMap(null, "argo") },
        el("span", { class: "nav-label", text: label }), el("span", { class: "n" }))
      : id === "warnings" ? el("a", { "data-kind": "map:events", class: "nav-overview", onclick: () => showMap(null, "events") },
        el("span", { class: "nav-label", text: label }), el("span", { class: "n" }))
      : id === "overview" ? el("a", { "data-kind": "overview", class: "nav-overview", onclick: () => showOverview() },
        el("span", { class: "nav-label", text: label }), el("span", { class: "n" }))
      : id === "map" ? el("a", { "data-kind": "map", class: "nav-overview", onclick: () => showMap() },
        el("span", { class: "nav-label", text: label }), el("span", { class: "n" })) : link(id, label));
    if (links.length) nav.append(section(name, icon, links));
  }
  nav.append(section("Helm", "⎈", [
    el("a", { "data-kind": "helm:releases", onclick: () => showHelm("releases") }, el("span", { class: "nav-label", text: "Releases" }), el("span", { class: "n" })),
    el("a", { "data-kind": "helm:charts", onclick: () => showHelm("charts") }, el("span", { class: "nav-label", text: "Charts" }), el("span", { class: "n" })),
  ]));
  // Every other CRD, grouped by API group, so nothing the cluster serves is hidden.
  const listed = new Set(NAV.flatMap(([, , items]) => items.map(([id]) => id)));
  const crds = S.kinds.filter((k) => k.group && k.group.includes(".") && !listed.has(kindId(k)) &&
    !/(^|\.)k8s\.io$/.test(k.group));
  if (crds.length) {
    const groups = new Map();
    for (const k of crds) (groups.get(k.group) || groups.set(k.group, []).get(k.group)).push(k);
    const body = [...groups.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([g, ks]) => {
      const open = sectionOpen("crd:" + g, ks.some((k) => kindId(k) === current));
      return el("div", { class: "sec sub" + (open ? " open" : ""), "data-sec": "crd:" + g },
        el("button", { type: "button", class: "sec-head", "aria-expanded": String(open),
            onclick: (e) => { const s = e.currentTarget.parentElement; toggleSection("crd:" + g, !s.classList.contains("open")); } },
          el("span", { class: "sec-chev", text: "›" }), el("span", { class: "sec-name", text: g }), el("span", { class: "sec-count", text: String(ks.length) })),
        el("div", { class: "sec-body" }, ...ks.sort((a, b) => a.kind.localeCompare(b.kind)).map((k) => link(kindId(k), k.kind))));
    });
    const open = sectionOpen("Custom Resources", crds.some((k) => kindId(k) === current));
    nav.append(el("div", { class: "sec" + (open ? " open" : ""), "data-sec": "Custom Resources" },
      sectionHead("Custom Resources", "✦", crds.length, open), el("div", { class: "sec-body" }, ...body)));
  }
  markNav();
}

function markNav() {
  try { if (typeof Tabs !== "undefined" && !S.empty) queueMicrotask(Tabs.note); } catch (_) {}
  if ($("#pfCount") && $("#pfCount").dataset.n == null) { $("#pfCount").dataset.n = "0"; refreshForwardCount(); }
  for (const a of document.querySelectorAll("#nav a[data-kind]")) {
    const on = S.map ? a.dataset.kind === "map:" + S.mapScope : S.overview ? a.dataset.kind === "overview" : S.helm ? a.dataset.kind === "helm:" + S.helm : S.kind && a.dataset.kind === S.kind.id;
    a.classList.toggle("on", on);
    const n = a.querySelector(".n");
    if (n) n.textContent = on && !S.map && !S.overview && S.rows.length ? String(S.rows.length) : "";
  }
}

// Helm releases and charts behave like any other list (filter, sort, health tabs,
// all-clusters), fed from the Helm endpoints instead of the Kubernetes API.
const HELM_KINDS = {
  releases: { id: "helm:releases", kind: "Helm Release", name: "releases", namespaced: true, verbs: [], helm: "releases",
    cols: ["CHART", "VERSION", "APP-VERSION", "REVISION", "UPDATED"] },
  charts: { id: "helm:charts", kind: "Helm Chart", name: "charts", namespaced: false, verbs: [], helm: "charts",
    cols: ["VERSIONS", "RELEASES", "DESCRIPTION"] },
};
function helmRows(which, data, context) {
  if (which === "releases") return data.map((r) => ({
    context, namespace: r.namespace, name: r.name, status: r.status, health: r.health, labels: {},
    created: r.updated, age: ageOf(r.updated), helm: r,
    cells: { CHART: r.chart, VERSION: r.chartVersion, "APP-VERSION": r.appVersion || "-", REVISION: String(r.revision), UPDATED: ageOf(r.updated) + " ago" },
  }));
  return data.map((c) => ({
    context, namespace: "", name: c.name, status: c.versions.length > 1 ? `${c.versions.length} versions` : "", health: c.versions.length > 1 ? "warn" : "",
    labels: {}, created: "", age: "", helm: c,
    cells: { VERSIONS: c.versions.join(", "), RELEASES: c.releases.length + " · " + c.releases.slice(0, 3).join(", ") + (c.releases.length > 3 ? "…" : ""), DESCRIPTION: c.description || "" },
  }));
}
function ageOf(t) {
  const d = (Date.now() - new Date(t).getTime()) / 1000;
  if (!isFinite(d)) return "";
  return d < 60 ? `${Math.round(d)}s` : d < 3600 ? `${Math.round(d / 60)}m` : d < 172800 ? `${Math.round(d / 3600)}h` : `${Math.round(d / 86400)}d`;
}
async function showHelm(which) {
  leaveOverview();
  S.helm = which;
  S.kind = { ...HELM_KINDS[which] };
  S.sort = null; S.sel = -1; S.rows = []; S.picked.clear(); S.health = "all";
  setFilter("", true);
  closePanel();
  render();
  await load();
}

async function setKind(id) {
  leaveOverview();
  if (id && id.startsWith("helm:")) return showHelm(id.slice(5));
  S.helm = null;
  const k = findKind(id);
  if (!k) return toast(`This cluster has no resource type called ${id}.`, true);
  const changed = !S.kind || S.kind.id !== kindId(k);
  S.kind = { ...k, id: kindId(k) };
  store.set("kind", S.kind.id);
  S.sort = null; S.sel = -1; S.rows = []; S.picked.clear();
  // A filter written for pods (restarts>0, -ns:kube-system) matches nothing on a
  // cluster-scoped CRD like NodeClaims, and the list looked broken. Each list starts clean.
  if (changed) { S.health = "all"; setFilter("", true); }
  closePanel();
  render();
  await load();
}

function setEverywhere(on) {
  leaveEmpty();
  S.everywhere = on;
  S.onlyCtx = null; S.errorsOpen = false; S.picked.clear();
  $("#ctxAll").hidden = !on;
  renderCtxList();
  $("#btnEverywhere").classList.toggle("on", on);
  $("#everyLabel").textContent = on ? "This cluster only" : "All clusters";
  load();
}

async function load() {
  if (!S.kind || S.overview || S.map || S.empty) return;
  clearTimeout(S.timer);
  if (S.nsNone) { S.rows = []; S.errors = []; setSpin(false); return render(); }
  const seq = ++S.seq;
  setSpin(true);
  try {
    if (S.helm) {
      const ctxs = S.everywhere ? S.contexts.map((c) => c.name) : [S.ctx];
      const res = await Promise.all(ctxs.map((c) => api(`/api/helm/${S.helm}?` + qs({ context: c, namespace: S.ns }))
        .then((d) => ({ c, d })).catch((e) => ({ c, e }))));
      if (seq !== S.seq) return;
      S.rows = []; S.errors = []; S.cols = S.kind.cols;
      for (const { c, d, e } of res) {
        if (e) S.errors.push(`${short(c)}: ${e.message.split("\n")[0]}`);
        else S.rows.push(...helmRows(S.helm, d, c));
      }
    } else if (S.everywhere) {
      const res = await api("/api/everywhere?" + qs({ kind: S.kind.id, namespace: S.ns }));
      if (seq !== S.seq) return;
      S.rows = []; S.errors = []; S.cols = [];
      for (const r of res) {
        if (r.error) S.errors.push(`${short(r.context)}: ${r.error.split("\n")[0]}`);
        if (!S.cols.length && r.columns) S.cols = r.columns;
        for (const row of r.rows || []) S.rows.push({ ...row, context: r.context });
      }
    } else {
      const r = await api("/api/list?" + qs({ context: S.ctx, kind: S.kind.id, namespace: S.ns }));
      if (seq !== S.seq) return;
      S.rows = r.rows.map((x) => ({ ...x, context: S.ctx }));
      S.cols = r.columns || []; S.errors = [];
    }
    S.loadedAt = Date.now();
  } catch (e) {
    if (seq !== S.seq) return;
    S.rows = []; S.errors = [e.message];
  } finally {
    if (seq === S.seq) setSpin(false);
  }
  render();
  S.timer = setTimeout(function again() { document.hidden ? (S.timer = setTimeout(again, 3000)) : load(); },
    S.helm ? 30000 : S.everywhere ? 20000 : 5000);
}

function setSpin(on) { S.loading = on; $("#spin").hidden = !on; }
function tickUpdated() {
  if (!S.loadedAt) return;
  const s = Math.round((Date.now() - S.loadedAt) / 1000);
  $("#updated").textContent = s < 3 ? "Live" : `Updated ${s}s ago`;
}

// ------------------------------------------------------------------ filter
function setFilter(v, syncInput = true) {
  S.filter = v;
  if (syncInput) $("#filter").value = v;
  $("#filterClear").hidden = !v;
  S.sel = -1;
  render();
}
function addTerm(t) {
  const terms = KCFilter.tokenize(S.filter);
  const i = terms.indexOf(t);
  if (i >= 0) terms.splice(i, 1); else terms.push(t);
  setFilter(terms.map((x) => (/\s/.test(x) ? `"${x}"` : x)).join(" "));
}
function toggleFilterHelp() {
  const h = $("#filterHelp");
  h.hidden = !h.hidden;
  $("#filterHelpBtn").setAttribute("aria-expanded", String(!h.hidden));
}
function buildFilterHelp() {
  $("#filterHelp").replaceChildren(el("h4", { text: "Filter syntax · click an example to add it" }),
    ...FILTER_HELP.map(([code, what]) => el("div", { class: "ex", onclick: () => { addTerm(code.replace(/^"|"$/g, "")); } },
      el("code", { text: code }), el("span", { text: what }))));
}
function renderChips() {
  const chips = KCFilter.describe(S.filter);
  $("#chips").replaceChildren(...chips.map((c) => el("span", { class: "chip" + (c.neg ? " neg" : "") },
    el("span", { class: "k", text: c.neg ? "not " + c.kind : c.kind }), c.text.replace(/^-/, ""),
    el("button", { type: "button", title: "Remove", text: "✕", onclick: () => addTerm(c.text) }))));
  const quick = QUICK[S.kind && S.kind.id] || [["Unhealthy", "!ok"], ["New (<1h)", "age<1h"]];
  const active = new Set(KCFilter.tokenize(S.filter));
  $("#quick").replaceChildren(...quick.map(([label, t]) =>
    el("button", { type: "button", class: "q" + (active.has(t) ? " on" : ""), text: label, onclick: () => addTerm(t) })));
}

// ------------------------------------------------------------------ table
function columns() {
  const c = [];
  if (S.kind && (S.kind.verbs || []).includes("delete")) c.push({ name: "", cls: "pick", get: () => "" });
  if (S.everywhere) c.push({ name: "CLUSTER", get: (r) => short(r.context), cls: "ctx" });
  // Like Lens: the name first, then where it lives. The namespace is a link that
  // narrows the list to it.
  c.push({ name: "NAME", get: (r) => r.name, cls: "name" });
  if (S.kind && S.kind.namespaced) c.push({ name: "NAMESPACE", get: (r) => r.namespace, cls: "ns-link" });
  if (S.rows.some((r) => r.status && r.status !== "-")) c.push({ name: "STATUS", get: (r) => r.status, cls: "status" });
  const podUsage = S.kind && S.kind.id === "pods";
  for (const col of S.cols) {
    if (podUsage && (col === "CPU" || col === "MEMORY")) {
      c.push({ name: col, get: (r) => (r.cells || {})[col] ?? "", key: (r) => quantity((r.cells || {})[col]), cls: "usage" });
      continue;
    }
    const numeric = /^(RESTARTS|DESIRED|READY|UP-TO-DATE|NODES|CPU|MIN|MAX|REPLICAS|LIMIT-CPU)$/.test(col);
    c.push({ name: col, get: (r) => (r.cells || {})[col] ?? "", cls: numeric ? "num mono" : "mono" });
  }
  if (!(S.kind && S.kind.helm)) c.push({ name: "AGE", get: (r) => r.age, key: (r) => -new Date(r.created).getTime(), cls: "num mono" });
  return c;
}

function render() {
  const cols = columns();
  const f = KCFilter.parse(S.filter);
  const base = S.rows.filter((r) => f.match(r));
  const counts = { all: base.length, ok: 0, warn: 0, bad: 0 };
  for (const r of base) if (counts[r.health] != null) counts[r.health]++;
  const inView = S.everywhere && S.onlyCtx ? base.filter((r) => r.context === S.onlyCtx) : base;
  let rows = S.health === "all" ? inView : inView.filter((r) => r.health === S.health);
  if (S.sort != null && cols[S.sort]) {
    const c = cols[S.sort], k = c.key || c.get;
    rows = rows.slice().sort((a, b) => {
      const x = k(a), y = k(b), nx = parseFloat(x), ny = parseFloat(y);
      const both = !isNaN(nx) && !isNaN(ny);
      return (both ? nx - ny : String(x).localeCompare(String(y), undefined, { numeric: true })) * S.sortDir;
    });
  }
  // Across clusters, keep each cluster's rows together (sorting stays within a cluster).
  if (S.everywhere) {
    const order = new Map(S.contexts.map((c, i) => [c.name, i]));
    rows = rows.map((r, i) => [r, i]).sort((a, b) => (order.get(a[0].context) - order.get(b[0].context)) || (a[1] - b[1])).map((x) => x[0]);
  }
  S.visible = rows;
  if (S.sel >= rows.length) S.sel = rows.length - 1;
  renderClusters(base);
  renderSelBar();

  renderHealth(counts);
  renderChips();
  markNav();
  if (S.map || S.overview) return; // a list load finishing late must not retitle the map / overview
  $("#crumbKind").textContent = S.kind ? S.kind.kind + (S.kind.kind.endsWith("s") ? "" : "s") : "";
  const where = S.everywhere ? (S.onlyCtx ? `${short(S.onlyCtx)} (of every cluster)` : `every cluster`) : short(S.ctx);
  $("#crumbWhere").textContent = `${where} · ${nsLabel()} · ${rows.length} of ${S.rows.length}`;
  renderErrors();

  $("#table thead").replaceChildren(el("tr", {}, cols.map((c, i) => c.cls === "pick" ? pickHead(rows) :
    el("th", { class: c.cls.includes("num") ? "num" : c.cls === "pick" ? "pick" : "", onclick: () => { S.sortDir = S.sort === i ? -S.sortDir : 1; S.sort = i; render(); } },
      c.name, S.sort === i ? el("span", { class: "arrow", text: S.sortDir > 0 ? "↑" : "↓" }) : null))));

  const tbody = $("#table tbody");
  if (!rows.length) {
    if (S.loading && !S.rows.length) {
      tbody.replaceChildren(...Array.from({ length: 8 }, () =>
        el("tr", { class: "skeleton" }, cols.map(() => el("td", {}, el("span"))))));
    } else {
      const why = S.errors.length ? ["Could not load this list", "See the message above."]
        : S.rows.length ? ["No rows match", "Change the filter or the health tab, or press Esc to clear it."]
        : S.nsNone ? ["No namespace selected", "Tick one or more namespaces in the sidebar."]
        : ["Nothing here", `No ${S.kind ? S.kind.kind : "objects"} in ${S.ns ? nsLabel() : "any namespace"}.`];
      tbody.replaceChildren(el("tr", {}, el("td", { colspan: cols.length },
        el("div", { class: "empty" }, el("strong", { text: why[0] }), why[1]))));
    }
    return;
  }
  const frag = document.createDocumentFragment();
  let group = null;
  rows.forEach((r, i) => {
    if (S.everywhere && r.context !== group) {
      group = r.context;
      const n = rows.filter((x) => x.context === group).length;
      frag.append(el("tr", { class: "group" }, el("td", { colspan: cols.length },
        el("div", { class: "group-head", style: `--h:${ctxHue(group)}`, title: group, oncontextmenu: (e) => clusterMenu(e, group) }, ctxTag(group, true),
          el("span", { class: "muted", text: `${n} ${n === 1 ? "row" : "rows"}` }),
          !S.onlyCtx ? el("button", { type: "button", class: "ghost small", text: "Only this cluster",
            onclick: (e) => { e.stopPropagation(); S.onlyCtx = group; S.sel = -1; render(); } }) : null))));
    }
    const picked = S.picked.has(rowKey(r));
    const tr = el("tr", { class: (i === S.sel ? "sel" : "") + (picked ? " picked" : ""), onclick: () => { S.sel = i; render(); S.helm ? openHelm(r) : openPanel(r); } });
    for (const c of cols) {
      const v = String(c.get(r) ?? "");
      let td;
      if (c.cls === "pick") {
        const box = el("input", { type: "checkbox", "aria-label": `Select ${r.name}`, checked: picked });
        td = el("td", { class: "pick", onclick: (e) => { e.stopPropagation(); togglePick(rows, i, e.shiftKey); } }, box);
      } else if (c.cls === "status") td = el("td", { class: "status-cell" }, el("span", { class: "pill " + (r.health || ""), text: v || "—", title: v }));
      else if (c.cls === "ctx") td = el("td", {}, ctxTag(r.context));
      else if (c.cls === "usage") td = usageCell(v, c.name);
      else if (c.cls === "ns-link") td = el("td", { class: "ns-link" }, el("a", { href: "#", text: v, title: `Show only ${v}`,
        onclick: (e) => { e.preventDefault(); e.stopPropagation(); S.ns = v; $("#ns").value = v; store.set("ns", v); syncNs(); load(); } }));
      else {
        td = el("td", { class: c.cls, text: v, title: v });
        if (c.name === "RESTARTS" && parseInt(v, 10) > 0) td.classList.add("hot");
      }
      tr.append(td);
    }
    frag.append(tr);
  });
  tbody.replaceChildren(frag);
}

// The all-clusters summary: one chip per cluster with its count and failing count.
// Click one to see only that cluster; click again (or All) to go back.
function renderClusters(base) {
  const box = $("#clusters");
  box.hidden = !S.everywhere;
  if (!S.everywhere) return;
  const per = new Map();
  for (const r of base) {
    const p = per.get(r.context) || { n: 0, bad: 0 };
    p.n++; if (r.health === "bad" || r.health === "warn") p.bad++;
    per.set(r.context, p);
  }
  const failed = new Set(S.errors.map((e) => e.split(": ")[0]));
  const chips = [el("button", { type: "button", class: "cl-chip" + (!S.onlyCtx ? " on" : ""), onclick: () => { S.onlyCtx = null; render(); } },
    el("span", { text: "All clusters" }), el("b", { text: String(base.length) }))];
  for (const c of S.contexts) {
    const p = per.get(c.name);
    const down = failed.has(short(c.name));
    if (!p && !down) continue;
    chips.push(el("button", { type: "button", class: "cl-chip" + (S.onlyCtx === c.name ? " on" : "") + (down ? " down" : ""),
      style: `--h:${ctxHue(c.name)}`, title: (down ? "Could not reach this cluster" : c.name) + " · right-click for options",
      oncontextmenu: (e) => clusterMenu(e, c.name),
      onclick: () => { if (down) return; S.onlyCtx = S.onlyCtx === c.name ? null : c.name; S.sel = -1; render(); } },
      el("span", { class: "ctx-dot" + (c.protected ? " prot" : "") }), short(c.name),
      down ? el("span", { class: "muted", text: "unreachable" }) : el("b", { text: String(p.n) }),
      p && p.bad ? el("span", { class: "cl-bad", text: `${p.bad} ✕` }) : null));
  }
  box.replaceChildren(...chips);
}

// Unreachable clusters fold into one line; the full messages are a click away.
function renderErrors() {
  const box = $("#errors");
  if (!S.errors.length) return box.replaceChildren();
  const names = S.errors.map((e) => e.split(": ")[0]);
  const reason = (e) => {
    const m = e.match(/no such host|i\/o timeout|connection refused|Unauthorized|expired|forbidden/i);
    return m ? m[0].toLowerCase() : e.split(": ").slice(1).join(": ").slice(0, 120);
  };
  const head = el("div", { class: "err" },
    el("span", {}, el("b", { text: S.everywhere ? `${S.errors.length} unreachable: ` : "" }),
      S.everywhere ? names.join(", ") + " — " + [...new Set(S.errors.map(reason))].join(", ") : S.errors[0]),
    S.everywhere ? el("button", { type: "button", class: "ghost small", text: S.errorsOpen ? "Hide details" : "Details",
      onclick: () => { S.errorsOpen = !S.errorsOpen; renderErrors(); } }) : null);
  const hint = S.everywhere && S.errors.some((e) => /no such host|i\/o timeout/i.test(e))
    ? el("div", { class: "err-hint", text: "\"no such host\" or a timeout usually means a private API endpoint you can only reach on the VPN." }) : null;
  box.replaceChildren(...[head, hint, S.errorsOpen ? el("pre", { class: "err-detail", text: S.errors.join("\n") }) : null].filter(Boolean));
}

// The pods a workload owns, found by its selector, opened together in one log tab.
async function workloadLogs(row, kind) {
  if (!row) return;
  try {
    const pods = await api("/api/pods-of?" + qs({ context: row.context, kind: kind.id, namespace: row.namespace, name: row.name }));
    if (!pods.length) return toast(`${row.name} has no pods right now.`, true);
    if (pods.length > Dock.MAX_LOG_PODS) toast(`${row.name} has ${pods.length} pods; showing the first ${Dock.MAX_LOG_PODS}.`);
    Dock.logs(pods.map((p) => ({ ...p, context: row.context })));
  } catch (e) { toast(e.message, true); }
}

// ------------------------------------------------------------------ selection
let lastPick = -1;
function togglePick(rows, i, range) {
  const r = rows[i];
  const on = !S.picked.has(rowKey(r));
  // Shift-click ticks (or unticks) everything between the last click and this one.
  const [a, b] = range && lastPick >= 0 ? [Math.min(lastPick, i), Math.max(lastPick, i)] : [i, i];
  for (let j = a; j <= b; j++) on ? S.picked.set(rowKey(rows[j]), rows[j]) : S.picked.delete(rowKey(rows[j]));
  lastPick = i;
  render();
}

function pickHead(rows) {
  const vis = rows.filter((r) => S.picked.has(rowKey(r))).length;
  const box = el("input", { type: "checkbox", "aria-label": "Select all shown" });
  box.checked = vis > 0 && vis === Math.min(rows.length, MAX_BULK);
  box.indeterminate = vis > 0 && !box.checked;
  box.onclick = (e) => {
    e.stopPropagation();
    // Anything ticked: clear. Nothing ticked: tick what is shown, up to the cap.
    if (S.picked.size) S.picked.clear();
    else {
      rows.slice(0, MAX_BULK).forEach((r) => S.picked.set(rowKey(r), r));
      if (rows.length > MAX_BULK) toast(`Selected the first ${MAX_BULK}. Narrow the filter to pick the rest.`);
    }
    lastPick = -1;
    render();
  };
  return el("th", { class: "pick", title: "Select all shown (filter first to narrow)" }, box);
}

function renderSelBar() {
  const n = S.picked.size;
  $("#selBar").hidden = n === 0;
  if (!n) return;
  const items = [...S.picked.values()];
  const ctxs = [...new Set(items.map((r) => r.context))];
  const prot = ctxs.filter(isProtected);
  $("#selCount").textContent = `${n} selected`;
  $("#selWhere").textContent = `${S.kind.kind} · ${ctxs.length === 1 ? short(ctxs[0]) : ctxs.length + " clusters"}` +
    "";
  $("#selDelete").textContent = `Delete ${n}…`;
  const lb = $("#selLogs");
  lb.hidden = S.kind.id !== "pods";
  lb.textContent = n > Dock.MAX_LOG_PODS ? `Logs (first ${Dock.MAX_LOG_PODS})` : `Logs of ${n}`;
}

async function deleteMany() {
  const items = [...S.picked.values()];
  if (!items.length) return;
  if (items.length > MAX_BULK) return toast(`Delete at most ${MAX_BULK} at a time.`, true);
  const prot = items.some((r) => isProtected(r.context));
  const phrase = "delete";
  const sample = items.slice(0, 6).map((r) => `${short(r.context)}/${r.namespace ? r.namespace + "/" : ""}${r.name}`).join("\n") +
    (items.length > 6 ? `\n…and ${items.length - 6} more` : "");
  const confirm = await ask({
    title: `Delete ${items.length} ${S.kind.kind}${items.length === 1 ? "" : "s"}?`,
    text: sample,
    want: prot ? phrase : null, ok: `Delete ${items.length}`, danger: true,
  });
  if (confirm == null) return;
  try {
    const r = await post("/api/delete-many", { kind: S.kind.id, confirm: prot ? confirm : "",
      items: items.map((x) => ({ context: x.context, namespace: x.namespace, name: x.name })) });
    const failed = r.results.filter((x) => x.error);
    if (failed.length) toast(`${r.ok}. ${failed.length} failed, first: ${failed[0].name}: ${failed[0].error}`, true);
    else toast(r.ok.charAt(0).toUpperCase() + r.ok.slice(1));
    S.picked.clear();
    for (const f of failed) S.picked.set(rowKey(f), items.find((x) => rowKey(x) === rowKey(f)) || f);
  } catch (e) { toast(e.message, true); }
  load();
}

function renderHealth(counts) {
  // Kinds with no health of their own (Secrets, ConfigMaps, RBAC...) get just the count.
  const noHealth = counts.all > 0 && !counts.ok && !counts.warn && !counts.bad;
  const tabs = noHealth ? [["all", "All"]] : [["all", "All"], ["bad", "Failing"], ["warn", "Warning"], ["ok", "Healthy"]];
  $("#health").replaceChildren(...tabs.map(([id, label]) => el("button", {
    type: "button", "aria-pressed": String(S.health === id),
    onclick: () => { S.health = id; S.sel = -1; render(); },
  }, id !== "all" ? el("span", { class: "sw " + id }) : null, label, el("b", { text: String(counts[id]) }))));
}

// ------------------------------------------------------------------ drawer
function openPanel(row, tab) {
  stopStreams();
  const kind = S.panel && S.panel.row === row ? S.panel.kind : S.kind;
  const isPod = kind.id === "pods";
  const hasData = kind.id === "secrets" || kind.id === "configmaps";
  S.panel = { row, kind, tab: tab || (isPod && row.health && row.health !== "ok" ? "why" : hasData ? "data" : "yaml") };
  $("#panel").hidden = false;
  $("#panelKind").replaceChildren(ctxTag(row.context, true), ` ${kind.kind}${row.namespace ? " · " + row.namespace : ""}`);
  $("#panelTitle").textContent = row.name;
  const tabs = isPod ? [["why", "Why?"], ["yaml", "YAML"], ["logs", "Logs"], ["shell", "Shell"], ["files", "Files"]]
    : kind.id === "nodes" ? [["yaml", "YAML"], ["shell", "Node shell"], ["files", "Files"]]
    : hasData ? [["data", kind.id === "secrets" ? "Values" : "Data"], ["yaml", "YAML"]] : [["yaml", "YAML"]];
  if ((kind.verbs || []).includes("update")) tabs.push(["edit", "Edit"]);
  $("#panelTabs").replaceChildren(...tabs.map(([id, label]) => el("button", { role: "tab", type: "button",
    "aria-selected": String(S.panel.tab === id), text: label, onclick: () => openPanel(row, id) })));
  buildActions(row, kind);
  showTab();
}

function closePanel() { stopStreams(); S.panel = null; $("#panel").hidden = true; }

logSock = null; shellSock = null; term = null; termCleanup = null;
function stopStreams() {
  if (termCleanup) { termCleanup(); termCleanup = null; }
  if (logSock) { logSock.onclose = null; logSock.close(); logSock = null; }
  if (shellSock) { shellSock.onclose = null; shellSock.close(); shellSock = null; }
  if (term) { term.dispose(); term = null; }
}

function buildActions(row, kind) {
  const id = kind.id, verbs = kind.verbs || [];
  const b = (label, fn, cls) => el("button", { type: "button", class: cls || "", text: label, onclick: fn });
  const acts = [];
  if (["deployments.apps", "statefulsets.apps", "replicasets.apps"].includes(id)) acts.push(b("Scale…", () => scale(row)));
  if (["deployments.apps", "statefulsets.apps", "daemonsets.apps"].includes(id)) acts.push(b("Restart", () => restart(row)));
  if (id === "pods") acts.push(b("Shell", () => Dock.shell(row, false)), b("Logs", () => Dock.logs([row])), b("Port-forward…", () => forward(row)),
    b("Restart", () => restartPod(row)));
  if (id === "nodes") acts.push(b("Node shell", () => Dock.shell(row, true)));
  if (["deployments.apps", "statefulsets.apps", "daemonsets.apps", "replicasets.apps", "jobs.batch"].includes(id))
    acts.push(b("Logs of all pods", () => workloadLogs(row, kind)));
  if (id === "nodes") {
    const off = (row.status || "").includes("SchedulingDisabled");
    acts.push(b(off ? "Uncordon" : "Cordon", () => cordon(row, !off)), b("Drain…", () => drain(row)));
  }
  acts.push(b("Copy name", () => copy(row.name)));
  if (verbs.includes("delete")) acts.push(el("span", { style: "flex:1" }), b("Delete…", () => del(row), "danger"));
  $("#panelActions").replaceChildren(...acts);
}

async function showTab() {
  const { row, tab, kind } = S.panel;
  const body = $("#panelBody");
  body.replaceChildren(el("div", { class: "empty", text: "Loading…" }));
  const base = { context: row.context, kind: kind.id, namespace: row.namespace, name: row.name };
  try {
    if (tab === "yaml") {
      const r = await api("/api/yaml?" + qs(base));
      body.replaceChildren(el("div", { class: "pane" },
        el("div", { class: "tools" }, el("span", { class: "grow muted", text: `${r.yaml.split("\n").length} lines` }),
          el("button", { type: "button", text: "Copy", onclick: () => copy(r.yaml) })),
        yamlPre(r.yaml)));
    } else if (tab === "edit") {
      const r = await api("/api/yaml?" + qs(base));
      const ta = el("textarea", { class: "code", spellcheck: "false", id: "yamlEdit" });
      ta.value = r.yaml;
      body.replaceChildren(el("div", { class: "pane" },
        el("div", { class: "tools" },
          el("span", { class: "grow muted", text: "If someone changes this object before you apply, the apply is refused instead of overwriting their change." }),
          el("button", { type: "button", class: "primary", text: "Apply…", onclick: () => apply(row, ta.value) })),
        ta));
    } else if (tab === "files") {
      await showFiles(row, kind, body);
    } else if (tab === "data") {
      await (kind.id === "secrets" ? showSecret(row, body) : showConfigMap(row, body));
    } else if (tab === "why") {
      const why = await api("/api/explain?" + qs(base));
      body.replaceChildren(el("ul", { class: "why" }, why.map((w) =>
        el("li", { class: /^Nothing wrong/.test(w) ? "okay" : "", text: w }))));
    } else if (tab === "logs" && S.useDock) {
      Dock.logs([row]); return openPanel(row, "why");
    } else if (tab === "shell" && S.useDock) {
      Dock.shell(row, kind.id === "nodes"); return openPanel(row, kind.id === "nodes" ? "yaml" : "why");
    } else if (tab === "logs") {
      await showLogs(row, body);
    } else if (tab === "shell") {
      await showShell(row, body);
    }
  } catch (e) {
    body.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "Could not load" }), e.message));
  }
}

// Light YAML colouring: keys, strings, numbers, comments. Built from text nodes.
function yamlPre(text) {
  const pre = el("pre");
  for (const line of text.split("\n")) {
    const m = line.match(/^(\s*-?\s*)([^:#\s][^:#]*?)(:)(\s.*|)$/);
    if (/^\s*#/.test(line)) pre.append(el("span", { class: "y-com", text: line }));
    else if (m) {
      pre.append(m[1], el("span", { class: "y-key", text: m[2] }), m[3]);
      const v = m[4];
      if (/^\s*-?\d+(\.\d+)?\s*$/.test(v) || /^\s*(true|false|null)\s*$/.test(v)) pre.append(el("span", { class: "y-num", text: v }));
      else if (v.trim()) pre.append(el("span", { class: "y-str", text: v }));
    } else pre.append(line);
    pre.append("\n");
  }
  return pre;
}

async function showLogs(row, body) {
  const containers = await api("/api/containers?" + qs({ context: row.context, namespace: row.namespace, name: row.name }));
  const st = { container: containers[containers.length - 1], previous: false, follow: true, find: "", wrap: true, lines: [] };
  const pre = el("pre");
  const sel = el("select", { id: "logContainer" }, containers.map((c) => el("option", { value: c, text: c })));
  sel.value = st.container;
  const find = el("input", { id: "logFind", placeholder: "Find in logs", style: "width:180px" });
  const chk = (id, label, on) => el("label", { for: id }, el("input", { type: "checkbox", id, checked: on }), label);
  const prev = chk("logPrev", "Previous (crashed)", false), follow = chk("logFollow", "Follow", true);
  const count = el("span", { class: "grow muted" });
  const save = el("button", { type: "button", text: "Copy all", onclick: () => copy(st.lines.join("\n")) });
  body.replaceChildren(el("div", { class: "pane logs" }, el("div", { class: "tools" }, sel, find, prev, follow, count, save), pre));
  const draw = () => {
    const f = st.find.toLowerCase();
    const lines = (f ? st.lines.filter((l) => l.toLowerCase().includes(f)) : st.lines).slice(-4000);
    const frag = document.createDocumentFragment();
    for (const l of lines) {
      const r = KCLog.render(l, f);
      frag.append(el("div", { class: "log-line one" + (r.level ? " lv-" + r.level : "") }, r.node));
    }
    pre.replaceChildren(frag);
    count.textContent = f ? `${lines.length} of ${st.lines.length} lines` : `${st.lines.length} lines`;
    if (st.follow) pre.scrollTop = pre.scrollHeight;
  };
  const connect = () => {
    if (logSock) { logSock.onclose = null; logSock.close(); }
    st.lines = []; draw();
    logSock = new WebSocket(`ws://${location.host}/ws/logs?` + qs({ token: TOKEN, context: row.context, namespace: row.namespace,
      name: row.name, container: st.container, previous: st.previous, follow: !st.previous, tail: 1000 }));
    logSock.onmessage = (ev) => {
      if (ev.data.startsWith("\x00")) st.lines.push(ev.data.startsWith("\x00error:") ? "── " + ev.data.slice(1) : "── end of log");
      else st.lines.push(...ev.data.split("\n"));
      if (st.lines.length > 20000) st.lines.splice(0, st.lines.length - 20000);
      draw();
    };
  };
  sel.onchange = () => { st.container = sel.value; connect(); };
  find.oninput = () => { st.find = find.value; draw(); };
  prev.querySelector("input").onchange = (e) => { st.previous = e.target.checked; connect(); };
  follow.querySelector("input").onchange = (e) => { st.follow = e.target.checked; draw(); };
  pre.onscroll = () => { const at = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
    if (at !== st.follow) { st.follow = at; follow.querySelector("input").checked = at; } };
  connect();
}

// ------------------------------------------------------------------ secrets
// Values stay masked until revealed, one key or all. Fetching them is a POST that the
// server audits, and on a protected cluster it needs the secret's name typed -- the
// same bar as a write. The decoded values live only in this closure: closing the
// drawer drops them.
async function showSecret(row, body) {
  const gate = el("div", { class: "empty" },
    el("strong", { text: "Values are hidden" }),
    el("p", { text: "Showing them is recorded in the audit log (key names only, never values)." }),
    el("button", { type: "button", class: "primary", text: "Show values", onclick: async () => {
      try {
        const entries = await post("/api/secret", { context: row.context, namespace: row.namespace, name: row.name });
        renderEntries(body, entries, true);
      } catch (e) { toast(e.message, true); }
    } }));
  body.replaceChildren(gate);
}

async function showConfigMap(row, body) {
  const entries = await api("/api/configmap?" + qs({ context: row.context, namespace: row.namespace, name: row.name }));
  if (!entries.length) return body.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "No data keys" })));
  renderEntries(body, entries, false);
}

function renderEntries(body, entries, masked) {
  const shown = new Set();
  const list = el("div", { class: "kv" });
  const find = el("input", { id: "kvFind", placeholder: "Filter keys", style: "width:200px" });
  const toggleAll = el("button", { type: "button", text: masked ? "Reveal all" : "", hidden: !masked });
  const draw = () => {
    const f = find.value.toLowerCase();
    list.replaceChildren(...entries.filter((e) => !f || e.key.toLowerCase().includes(f)).map((e) => {
      const open = !masked || shown.has(e.key);
      const val = e.binary
        ? el("div", { class: "kv-val muted", text: `Binary value, ${e.size} bytes. Copy it from YAML with kubectl.` })
        : el("pre", { class: "kv-val" + (open ? "" : " masked"), text: open ? e.value : "•".repeat(Math.min(24, Math.max(8, e.size))) });
      const acts = el("div", { class: "kv-acts" },
        el("span", { class: "muted", text: `${e.size} B` }),
        masked && !e.binary ? el("button", { type: "button", class: "ghost", text: open ? "Hide" : "Show",
          onclick: () => { open ? shown.delete(e.key) : shown.add(e.key); draw(); } }) : null,
        !e.binary ? el("button", { type: "button", class: "ghost", text: "Copy", onclick: () => copy(e.value) }) : null);
      return el("div", { class: "kv-row" }, el("div", { class: "kv-head" }, el("code", { class: "kv-key", text: e.key }), acts), val);
    }));
    toggleAll.textContent = shown.size ? "Hide all" : "Reveal all";
  };
  toggleAll.onclick = () => { if (shown.size) shown.clear(); else entries.forEach((e) => shown.add(e.key)); draw(); };
  find.oninput = draw;
  body.replaceChildren(el("div", { class: "pane" },
    el("div", { class: "tools" }, find, el("span", { class: "grow muted", text: `${entries.length} keys` }), toggleAll), list));
  draw();
}

function termTheme() {
  return { background: "#0b0e13", foreground: "#e4e9f1", cursor: "#6f9bff", selectionBackground: "#2c3b5e" };
}

async function showShell(row, body) {
  const isNode = S.panel && S.panel.kind.id === "nodes";
  const containers = isNode ? [] : await api("/api/containers?" + qs({ context: row.context, namespace: row.namespace, name: row.name }));
  const box = el("div", { class: "term" });
  const status = el("div", { class: "term-status", text: isNode ? `Starting a shell on ${row.name}… a helper pod is being scheduled.` : `Connecting to ${row.name}…` });
  body.replaceChildren(el("div", { class: "term-wrap" }, status, box));
  term = new Terminal({ cursorBlink: true, fontSize: 13, fontFamily: "ui-monospace, 'SF Mono', Menlo, monospace", theme: termTheme() });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit); term.open(box); fit.fit();
  shellSock = new WebSocket(`ws://${location.host}/ws/exec?` + qs(isNode
    ? { token: TOKEN, context: row.context, node: row.name }
    : { token: TOKEN, context: row.context, namespace: row.namespace, name: row.name, container: containers[containers.length - 1] }));
  shellSock.binaryType = "arraybuffer";
  const sendSize = () => shellSock && shellSock.readyState === 1 && shellSock.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
  shellSock.onopen = () => { status.textContent = isNode ? `Root shell on ${row.name}. The helper pod is removed when you close this tab.` : `Shell in ${row.name}`; sendSize(); term.focus(); };
  shellSock.onmessage = (ev) => term && term.write(typeof ev.data === "string" ? ev.data : new Uint8Array(ev.data));
  shellSock.onclose = () => { status.textContent = "Session closed."; if (term) term.write("\r\n\x1b[90m[session closed]\x1b[0m\r\n"); };
  const enc = new TextEncoder();
  term.onData((d) => shellSock && shellSock.readyState === 1 && shellSock.send(enc.encode(d)));
  // fit() changes the terminal's rows, which can change the box, which fired the
  // observer again: the shell visibly shook. Debounce, and only act on a real change.
  let last = "", t = null;
  const ro = new ResizeObserver(() => {
    clearTimeout(t);
    t = setTimeout(() => {
      const key = `${box.clientWidth}x${box.clientHeight}`;
      if (!term || key === last || !box.clientWidth || !box.clientHeight) return;
      last = key;
      try { fit.fit(); sendSize(); } catch (_) {}
    }, 120);
  });
  ro.observe(box);
  termCleanup = () => ro.disconnect();
}

// ------------------------------------------------------------------ dialogs
// One in-page dialog for confirmations and small inputs. The server enforces the
// typed confirmation on protected clusters; this only collects it.
function ask({ title, text, row, want, input, value, label, ok = "Confirm", danger }) {
  return new Promise((resolve) => {
    $("#dialogTitle").textContent = title;
    $("#dialogText").textContent = text || "";
    $("#dialogText").hidden = !text;
    const where = $("#dialogWhere");
    where.textContent = "";
    where.hidden = true;
    const inp = $("#dialogInput"), lbl = $("#dialogLabel");
    const typed = !!want, asking = typed || input;
    inp.hidden = lbl.hidden = !asking;
    inp.value = value ?? "";
    lbl.textContent = typed ? `Type ${want} and confirm` : label || "";
    inp.placeholder = typed ? want : "";
    const btn = $("#dialogOk");
    btn.textContent = ok;
    btn.className = danger ? "danger" : "primary";
    const check = () => { btn.disabled = typed && inp.value !== want; };
    inp.oninput = check; check();
    $("#dialog").hidden = false;
    const done = (v) => { $("#dialog").hidden = true; document.removeEventListener("keydown", esc, true); resolve(v); };
    const esc = (e) => { if (e.key === "Escape") { e.stopPropagation(); done(null); } };
    document.addEventListener("keydown", esc, true);
    $("#dialogForm").onsubmit = (e) => { e.preventDefault(); if (!btn.disabled) done(asking ? inp.value : ""); };
    $("#dialogCancel").onclick = () => done(null);
    setTimeout(() => (asking ? inp : btn).focus(), 0);
  });
}

// On a protected cluster the object's name must be typed; elsewhere one click.
// Delete and apply (edit) need the resource's name typed; other changes are one click.
async function guarded(row, opts, call) {
  const confirm = await ask({ ...opts, row, want: opts.typeName ? row.name : null });
  if (confirm == null) return false;
  try {
    const r = await call(opts.typeName ? confirm : "");
    toast(r.ok ? r.ok.charAt(0).toUpperCase() + r.ok.slice(1) : "Done");
    if (r.failed && r.failed.length) toast(`${r.failed.length} pods were not evicted. First: ${r.failed[0]}`, true);
  } catch (e) { toast(e.message, true); return false; }
  load();
  return true;
}

const body = (row, extra) => ({ context: row.context, kind: (S.panel ? S.panel.kind : S.kind).id,
  namespace: row.namespace, name: row.name, ...extra });

async function del(row) {
  const done = await guarded(row, { title: `Delete ${row.name}?`, ok: "Delete", danger: true,
    typeName: true },
    (confirm) => post("/api/delete", body(row, { confirm })));
  if (done) closePanel();
}
async function scale(row) {
  const cur = ((row.cells || {}).READY || "").split("/")[1] || "1";
  const n = await ask({ title: `Scale ${row.name}`, input: true, label: "Replicas", value: cur, ok: "Next", row });
  if (n == null) return;
  const replicas = parseInt(n, 10);
  if (isNaN(replicas) || replicas < 0) return toast("Replicas must be a whole number, 0 or more.", true);
  guarded(row, { title: `Scale ${row.name} from ${cur} to ${replicas}?`, ok: "Scale" },
    (confirm) => post("/api/scale", body(row, { confirm, replicas })));
}
// A pod has no rollout restart: "restarting" it means deleting it so its Deployment,
// StatefulSet, DaemonSet or Job makes a new one (what kubectl users do). A bare pod
// with no owner would be gone for good, so that is said plainly first.
function restartPod(row) {
  const owned = !!(row.cells && row.cells.CONTROLLED_BY) || /-[a-z0-9]{5}$|-\d+$/.test(row.name);
  guarded(row, { title: `Restart ${row.name}?`, ok: "Restart", danger: !owned, typeName: true,
    text: owned ? "Deletes the pod; its controller starts a new one right away."
      : "This pod may have no controller: deleting it would not bring it back." },
    (confirm) => post("/api/delete", body(row, { confirm })));
}
function restart(row) {
  guarded(row, { title: `Restart ${row.name}?`, ok: "Restart", text: "Replaces every pod one by one, like kubectl rollout restart." },
    (confirm) => post("/api/restart", body(row, { confirm })));
}
async function apply(row, yaml) {
  const done = await guarded(row, { title: `Apply your edit to ${row.name}?`, ok: "Apply",
    typeName: true },
    (confirm) => post("/api/apply", body(row, { confirm, yaml })));
  if (done && S.panel) openPanel(row, "yaml");
}
function cordon(row, on) {
  guarded(row, { title: `${on ? "Cordon" : "Uncordon"} ${row.name}?`, ok: on ? "Cordon" : "Uncordon",
    text: on ? "New pods will not be scheduled on this node. Running pods stay." : "The node will accept new pods again." },
    (confirm) => post("/api/cordon", body(row, { confirm, on })));
}
function drain(row) {
  guarded(row, { title: `Drain ${row.name}?`, ok: "Drain", danger: true,
    text: "Cordons the node, then evicts every pod except DaemonSet pods. PodDisruptionBudgets are respected." },
    (confirm) => post("/api/drain", body(row, { confirm })));
}
async function forward(row) {
  const spec = await ask({ title: `Port-forward ${row.name}`, input: true, label: "Local port : container port (0 picks a free local port)",
    value: "0:8080", ok: "Start", row });
  if (!spec) return;
  const [local, remote] = spec.split(":").map((x) => parseInt(x, 10));
  if (isNaN(local) || isNaN(remote)) return toast("Use local:remote, for example 0:8080.", true);
  try {
    const f = await post("/api/forward", body(row, { local, remote }));
    toast(`Forwarding 127.0.0.1:${f.local} to ${row.name}:${f.remote}`);
    refreshForwardCount();
  } catch (e) { toast(e.message, true); }
}
async function refreshForwardCount() {
  try {
    const list = await api("/api/forwards");
    const p = $("#pfCount");
    if (p) { p.hidden = !list.length; p.textContent = String(list.length); }
    return list;
  } catch (_) { return []; }
}
async function showForwards() {
  stopStreams();
  const list = await refreshForwardCount();
  S.panel = null;
  $("#panel").hidden = false;
  $("#panelKind").textContent = "Local";
  $("#panelTitle").textContent = "Port Forwarding";
  $("#panelTabs").replaceChildren();
  $("#panelActions").replaceChildren();
  const b = $("#panelBody");
  if (!list.length) return b.replaceChildren(el("div", { class: "empty" }, el("strong", { text: "No port-forwards" }),
    "Open a pod and choose Port-forward to start one."));
  b.replaceChildren(el("ul", { class: "list" }, list.map((f) => el("li", {},
    el("a", { href: `http://127.0.0.1:${f.local}`, target: "_blank", rel: "noopener", text: `127.0.0.1:${f.local}` }),
    el("span", { class: "grow muted", text: `→ ${f.namespace}/${f.pod}:${f.remote} · ${short(f.context)}` }),
    el("button", { type: "button", text: "Stop", onclick: async () => { await post("/api/unforward", { id: f.id }); showForwards(); } })))));
}

async function copy(text) {
  try { await navigator.clipboard.writeText(text); toast("Copied"); }
  catch (_) { toast("Could not copy. Select the text and press ⌘C.", true); }
}

// ------------------------------------------------------------------ palette
let palItems = [], palSel = 0;
function openPalette() {
  $("#palette").hidden = false;
  const inp = $("#paletteInput");
  inp.value = "";
  inp.oninput = () => paletteRender();
  inp.onkeydown = (e) => {
    if (e.key === "ArrowDown") { palSel = Math.min(palItems.length - 1, palSel + 1); paletteRender(true); e.preventDefault(); }
    else if (e.key === "ArrowUp") { palSel = Math.max(0, palSel - 1); paletteRender(true); e.preventDefault(); }
    else if (e.key === "Enter") { const it = palItems[palSel]; closePalette(); if (it) it.run(); }
    else if (e.key === "Escape") closePalette();
  };
  $("#palette").onclick = (e) => { if (e.target.id === "palette") closePalette(); };
  paletteRender();
  setTimeout(() => inp.focus(), 0);
}
function closePalette() { $("#palette").hidden = true; }
function paletteRender(keepSel) {
  const q = $("#paletteInput").value.trim().toLowerCase();
  const items = [
    ...S.kinds.map((k) => ({ sec: "Resources", label: k.kind, hint: kindId(k), keys: [kindId(k), k.kind, ...(k.shortNames || [])].join(" "), run: () => setKind(kindId(k)) })),
    ...S.contexts.map((c) => ({ sec: "Clusters", label: short(c.name), hint: "", keys: "ctx cluster " + c.name, run: () => switchContext(c.name) })),
    { sec: "Actions", label: "Show only unhealthy", hint: "!ok", keys: "unhealthy failing broken !ok", run: () => setFilter("!ok") },
    { sec: "Actions", label: S.everywhere ? "This cluster only" : "This list in every cluster", hint: "all", keys: "all every clusters", run: () => setEverywhere(!S.everywhere) },
    { sec: "Actions", label: "Start screen", hint: "choose a cluster", keys: "home start welcome clusters choose", run: showWelcome },
    { sec: "Actions", label: "Create a resource", hint: "N", keys: "create new add yaml apply plus", run: () => openCreate() },
    { sec: "Resources", label: "Namespace Overview", hint: "get all", keys: "all everything namespace overview get all", run: () => showOverview() },
    { sec: "Resources", label: "Cluster Topology", hint: "nodes ↔ namespaces", keys: "topology map graph diagram topology visual cluster nodes pools", run: () => showMap(null, "cluster") },
    { sec: "Resources", label: "Warnings", hint: "cluster events, grouped", keys: "warnings events failing problems errors scheduling mount backoff dashboard", run: () => showMap(null, "events") },
    { sec: "Resources", label: "Argo CD Dashboard", hint: "apps, health, sync", keys: "argo argocd gitops dashboard applications sync health", run: () => showMap(null, "argo") },
    { sec: "Resources", label: "Cost Overview", hint: "OpenCost", keys: "cost money spend price opencost kubecost waste savings", run: () => showMap(null, "cost") },
    { sec: "Resources", label: "Helm Topology", hint: "release → resources", keys: "helm topology map release resources created owns", run: () => showMap(null, "helm") },
    { sec: "Resources", label: "Namespace Topology", hint: "one namespace", keys: "topology map graph diagram topology visual namespace", run: () => showMap(null, "namespace") },
    { sec: "Actions", label: "Settings", hint: "⌘,", keys: "settings preferences options", run: showSettings },
    { sec: "Actions", label: "Keyboard shortcuts", hint: "⌘/", keys: "keys keyboard shortcuts help", run: showShortcuts },
    { sec: "Resources", label: "Helm releases", hint: "helm", keys: "helm releases list", run: () => showHelm("releases") },
    { sec: "Resources", label: "Helm charts", hint: "helm", keys: "helm charts", run: () => showHelm("charts") },
    { sec: "Actions", label: "Port Forwarding", hint: "", keys: "pf port forwards forwarding", run: showForwards },
    { sec: "Actions", label: "Filter syntax", hint: "?", keys: "help filter syntax", run: toggleFilterHelp },
    ...["system", "light", "dark"].map((t) => ({ sec: "Theme", label: `Theme: ${t[0].toUpperCase()}${t.slice(1)}`, hint: currentTheme() === t ? "current" : "", keys: "theme " + t, run: () => applyTheme(t) })),
  ];
  const words = q.split(/\s+/).filter(Boolean);
  palItems = (words.length ? items.filter((i) => words.every((w) => (i.keys + " " + i.label).toLowerCase().includes(w))) : items).slice(0, 80);
  if (!keepSel) palSel = 0;
  const list = [];
  let sec = "";
  palItems.forEach((it, i) => {
    if (it.sec !== sec) { sec = it.sec; list.push(el("li", { class: "sec", text: sec })); }
    list.push(el("li", { class: i === palSel ? "on" : "", "data-i": i,
      onmousemove: (e) => { if (palSel === i) return; palSel = i;
        for (const li of document.querySelectorAll("#paletteList li[data-i]")) li.classList.toggle("on", li.dataset.i === String(i)); },
      onmousedown: (e) => { e.preventDefault(); closePalette(); it.run(); } },
      el("span", { text: it.label }), el("span", { class: "hint", text: it.hint })));
  });
  if (!palItems.length) list.push(el("li", { class: "sec", text: "Nothing matches" }));
  $("#paletteList").replaceChildren(...list);
  const on = $("#paletteList .on");
  if (on) on.scrollIntoView({ block: "nearest" });
}

// ------------------------------------------------------------------ keys
// The terminal's keys, in a window: j/k move, Enter YAML, l logs, s shell,
// x why, e edit, Ctrl+D delete, / filter, : or ⌘K palette, R refresh.
function wireKeys() {
  document.addEventListener("keydown", (e) => {
    const a = document.activeElement;
    const typing = /INPUT|TEXTAREA|SELECT/.test(a.tagName) || a.closest(".xterm");
    if (!$("#welcome").hidden) return;
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); return openPalette(); }
    if (e.altKey && (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "w" && Tabs.count()) { e.preventDefault(); return Tabs.closeActive(); }
    if (!$("#dialog").hidden || !$("#palette").hidden) return;
    if (e.key === "Escape" && menuEl) return closeMenu();
    if (e.key === "Escape" && S.map && S.flow) { S.flow.back(); return; }
    if (e.key === "Escape" && !typing && S.picked.size) { S.picked.clear(); return render(); }
    if (e.key === "Escape") {
      if (a.id === "filter") { if (S.filter) setFilter(""); else a.blur(); return; }
      if (!typing) { if (!$("#filterHelp").hidden) return toggleFilterHelp(); return closePanel(); }
    }
    if (typing) return;
    const row = S.visible[S.sel];
    const pod = S.kind && S.kind.id === "pods";
    switch (e.key) {
      case "j": case "ArrowDown": S.sel = Math.min(S.visible.length - 1, S.sel + 1); render(); scrollSel(); e.preventDefault(); break;
      case "k": case "ArrowUp": S.sel = Math.max(0, S.sel - 1); render(); scrollSel(); e.preventDefault(); break;
      case "/": e.preventDefault(); $("#filter").focus(); break;
      case ":": e.preventDefault(); openPalette(); break;
      case "?": toggleFilterHelp(); break;
      case "Enter": case "y": if (row) openPanel(row, "yaml"); break;
      case "l": if (pod && S.picked.size) Dock.logs([...S.picked.values()]); else if (row && pod) Dock.logs([row]); break;
      case "s": if (row && (pod || S.kind.id === "nodes")) Dock.shell(row, S.kind.id === "nodes"); break;
      case "x": if (row && pod) openPanel(row, "why"); break;
      case "e": if (row) openPanel(row, "edit"); break;
      case "d": if (e.ctrlKey) { e.preventDefault(); if (S.picked.size) deleteMany(); else if (row) { S.panel = { row, kind: S.kind }; del(row); } } break;
      case " ": if (row && (S.kind.verbs || []).includes("delete")) { e.preventDefault(); togglePick(S.visible, S.sel, e.shiftKey); } break;
      case "a": if ((e.ctrlKey || e.metaKey) && S.visible.length) { e.preventDefault(); S.visible.slice(0, MAX_BULK).forEach((r) => S.picked.set(rowKey(r), r)); render(); } break;
      case "R": load(); break;
      case "n": case "N": if (!e.metaKey && !e.ctrlKey) { e.preventDefault(); openCreate(); } break;
      case "1": S.health = "all"; render(); break;
      case "2": S.health = "bad"; render(); break;
      case "3": S.health = "warn"; render(); break;
      case "4": S.health = "ok"; render(); break;
    }
  });
}
function scrollSel() { const r = $("#table tbody tr.sel"); if (r) r.scrollIntoView({ block: "nearest" }); }

// A download notice with "Show in Finder", since the file is written by the app.
function showSaved(path, msg) {
  const t = $("#toast");
  t.replaceChildren(msg + "  ", el("button", { type: "button", class: "toast-btn", text: navigator.platform.startsWith("Mac") ? "Show in Finder" : "Show",
    onclick: () => post("/api/files/reveal", { path }).catch((e) => toast(e.message, true)) }));
  t.className = ""; t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), 9000);
}

let toastTimer;
function toast(msg, err) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = err ? "err" : "";
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), err ? 8000 : 3200);
}


// ------------------------------------------------------------------ app menu
// The native window's menu bar (menu_darwin.m) sends its items here.
window.kcMenu = (action) => {
  if (!$("#welcome").hidden && !["settings", "shortcuts", "filter-help", "cli-help", "audit-log", "app-log", "backups", "home", "palette"].includes(action) && !action.startsWith("theme-")) return;
  closePalette();
  const openFile = (which) => post("/api/open-app-file", { which }).catch((e) => toast(e.message, true));
  ({
    settings: showSettings, shortcuts: showShortcuts, palette: openPalette,
    create: () => openCreate(), home: showWelcome, clusters: showClusters, forwards: showForwards,
    find: () => { if (!$("#filterArea").hidden) $("#filter").focus(); else openPalette(); },
    overview: () => showOverview(), "map-cluster": () => showMap(null, "cluster"), "map-namespace": () => showMap(null, "namespace"),
    helm: () => showHelm("releases"), everywhere: () => setEverywhere(!S.everywhere),
    back: () => history.back(), forward: () => history.forward(),
    refresh: () => (S.map ? showMap() : S.overview ? showOverview() : load()),
    "theme-system": () => applyTheme("system"), "theme-light": () => applyTheme("light"), "theme-dark": () => applyTheme("dark"),
    "filter-help": () => { if ($("#filterArea").hidden) leaveOverview(); if ($("#filterHelp").hidden) toggleFilterHelp(); },
    "cli-help": showCliHelp,
    "audit-log": () => openFile("audit-log"), "app-log": () => openFile("app-log"), backups: () => openFile("backups"),
  }[action] || (() => {}))();
};

// A simple sheet for Settings / Shortcuts / CLI help, reusing the dialog overlay look.
function sheet(title, ...content) {
  let o = $("#sheet");
  if (!o) { o = el("div", { id: "sheet", class: "overlay" }); document.body.append(o); }
  const close = () => { o.hidden = true; document.removeEventListener("keydown", esc, true); };
  const esc = (e) => { if (e.key === "Escape") { e.stopPropagation(); close(); } };
  document.addEventListener("keydown", esc, true);
  o.onclick = (e) => { if (e.target === o) close(); };
  o.replaceChildren(el("div", { class: "dialog sheet", role: "dialog", "aria-label": title },
    el("div", { class: "sheet-head" }, el("h3", { text: title }), el("button", { type: "button", class: "icon-btn small", title: "Close (Esc)", text: "✕", onclick: close })),
    ...content));
  o.hidden = false;
  setTimeout(() => { const b = o.querySelector("button, input, select"); if (b) b.focus(); }, 0);
  return close;
}

function showSettings() {
  const seg = (name, value, choices, set) => el("div", { class: "seg", role: "radiogroup", "aria-label": name },
    choices.map(([v, label]) => el("button", { type: "button", role: "radio", "aria-checked": String(value === v), text: label,
      onclick: (e) => { set(v); for (const b of e.currentTarget.parentElement.children) b.setAttribute("aria-checked", String(b === e.currentTarget)); } })));
  const row = (label, hint, control) => el("div", { class: "set-row" }, el("div", {}, el("strong", { text: label }), hint ? el("p", { class: "muted", text: hint }) : null), control);
  const hiddenN = (S.hiddenCtx || []).length;
  sheet("Settings",
    row("Appearance", "System follows macOS light / dark as it changes.", seg("Appearance", currentTheme(), [["system", "System"], ["light", "Light"], ["dark", "Dark"]], applyTheme)),
    row("Cluster topology", "Show kube-system, monitoring and other platform namespaces in the cluster topology.",
      seg("Platform namespaces", store.get("mapSystem") === "1" ? "1" : "0", [["0", "Hide"], ["1", "Show"]], (v) => { store.set("mapSystem", v === "1" ? "1" : null); if (S.map && S.mapScope === "cluster") showMap(null, "cluster"); })),
    row("Clusters", `${S.contexts.length} in the list${hiddenN > 0 ? `, ${hiddenN} hidden` : ""}. Every cluster except local ones (kind, minikube, Docker Desktop…) is protected: deletes and other changes need the name typed.`,
      el("button", { type: "button", text: "Manage clusters…", onclick: () => { $("#sheet").hidden = true; showClusters(); } })),
    row("Records", "Every change is written to the audit log.",
      el("div", { class: "set-btns" },
        el("button", { type: "button", text: "Audit log", onclick: () => window.kcMenu("audit-log") }),
        null)));
}

const SHORTCUTS = [
  ["Anywhere", [["⌘K  or  :", "Command palette"], ["⌘,", "Settings"], ["⌘N  or  N", "Create a resource from YAML"], ["⌘R  or  R", "Refresh"], ["⌘F  or  /", "Filter"], ["?", "Filter syntax"], ["Esc", "Close / clear"]]],
  ["In a list", [["j / k  ↓ / ↑", "Move"], ["Enter  or  y", "YAML"], ["l", "Logs (pods; with several picked, merged)"], ["s", "Shell (pods and nodes)"], ["x", "Why is it failing"], ["e", "Edit"], ["Space", "Pick for bulk delete"], ["⌘A", "Pick all shown"], ["Ctrl+D", "Delete"], ["1 2 3 4", "All / failing / warning / healthy"]]],
  ["Window", [["⌘+  ⌘−  ⌘0", "Zoom in / out / actual size"], ["⌃⌘F", "Full screen"], ["⌘M", "Minimize"], ["⌘W", "Close window"], ["⌘Q", "Quit"]]],
];
function showShortcuts() {
  sheet("Keyboard Shortcuts", el("div", { class: "keys-grid" }, SHORTCUTS.map(([sec, list]) => el("section", {},
    el("h4", { text: sec }), el("dl", {}, list.flatMap(([k, what]) => [el("dt", {}, el("kbd", { text: k })), el("dd", { text: what })]))))));
}
function showCliHelp() {
  const cmds = [["kv", "Terminal UI on the current cluster"], ["kv deploy", "Start on a kind (any name, short name or plural)"], ["kv --context NAME", "Start on another cluster"],
    ["kv ui", "Open this window"], ["kv help", "The full guide: keys, filter syntax, commands"], ["kv --version", "Version"]];
  sheet("Terminal Commands", el("p", { class: "muted", text: "kv is on your PATH (~/.local/bin). Inside the terminal UI, type : for commands like :ctx, :ns, :logs, :all." }),
    el("dl", { class: "cli-list" }, cmds.flatMap(([c, w]) => [el("dt", {}, el("code", { text: c })), el("dd", { text: w })])));
}

// ------------------------------------------------------------------ pod usage
// "120m 48%": the value, and the share of the limit (or request without a limit).
// The bar turns amber at 70% and red at 90% -- close to throttling or an OOM kill.
function usageCell(v, col) {
  if (!v || v === "-") return el("td", { class: "usage none", text: "—", title: "No metrics (metrics-server not reachable, or the pod isn't running)" });
  const [val, p] = v.split(" ");
  const pct = p ? parseInt(p, 10) : null;
  const lvl = pct == null ? "" : pct >= 90 ? "hot" : pct >= 70 ? "warm" : "";
  const title = `${col === "CPU" ? "CPU" : "Memory"} in use: ${val}` + (pct == null ? " (no request or limit set)" : ` — ${pct}% of its limit (or request, when no limit is set)`);
  return el("td", { class: "usage " + lvl, title },
    el("span", { class: "u-val", text: val }),
    pct == null ? el("span", { class: "u-pct muted", text: "" }) : el("span", { class: "u-bar" }, el("i", { style: `width:${Math.min(100, pct)}%` })),
    pct == null ? null : el("span", { class: "u-pct", text: pct + "%" }));
}
// Sort key for "250m", "1.5", "300Mi", "1.2Gi" (+ optional " 40%").
function quantity(v) {
  if (!v || v === "-") return -1;
  const m = /^([\d.]+)(m|Ki|Mi|Gi|Ti)?/.exec(v);
  if (!m) return -1;
  const n = parseFloat(m[1]);
  return { m: n / 1000, Ki: n * 2 ** 10, Mi: n * 2 ** 20, Gi: n * 2 ** 30, Ti: n * 2 ** 40 }[m[2]] ?? n;
}

// Start once every script (dock.js loads after this file) has run.
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot); else setTimeout(boot, 0);
