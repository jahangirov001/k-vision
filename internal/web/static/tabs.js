// Tabs over the main area, like Lens: every list or page you open ("Pods -
// shop-prod", "Cluster Topology - payments-dev") gets a tab with a close button. Click
// a tab to go back to that view -- its cluster, namespace, filter and scope -- and ✕
// to close it. Opening something already open selects its tab instead of adding one.
// The tab strip is remembered per window (localStorage).
"use strict";
const Tabs = (() => {
  let tabs = [];          // { key, title, ctx, ns, kind, view, scope, filter }
  let active = null;
  let restoring = false;  // while a tab is being reopened, don't record it again
  const MAX = 12;

  const strip = () => document.querySelector("#viewTabs");
  const save = () => { try { localStorage.setItem("kc.viewTabs", JSON.stringify({ tabs, active })); } catch (_) {} };
  const load = () => { try { const v = JSON.parse(localStorage.getItem("kc.viewTabs") || "null"); if (v && Array.isArray(v.tabs)) { tabs = v.tabs; active = v.active; } } catch (_) {} };

  // What is on screen now, as a tab.
  function current() {
    if (!S.ctx || S.empty) return null;
    const c = short(S.ctx);
    if (S.map) {
      const scope = S.mapScope || "cluster";
      const name = { cluster: "Cluster Topology", namespace: "Namespace Topology", helm: "Helm Topology", cost: "Cost", argo: "Argo CD" }[scope] || "Topology";
      return { key: `map:${scope}|${S.ctx}|${scope === "namespace" ? S.ns : ""}`, title: `${name}${scope === "namespace" && S.ns ? " · " + S.ns : ""} - ${c}`,
        ctx: S.ctx, ns: S.ns, view: "map", scope };
    }
    if (S.overview) return { key: `overview|${S.ctx}|${S.ns}`, title: `Namespace Overview${S.ns ? " · " + S.ns : ""} - ${c}`, ctx: S.ctx, ns: S.ns, view: "overview" };
    if (S.helm) return { key: `helm:${S.helm}|${S.ctx}`, title: `Helm ${S.helm === "charts" ? "Charts" : "Releases"} - ${c}`, ctx: S.ctx, ns: S.ns, view: "helm", kind: S.helm };
    if (S.kind) {
      const every = S.everywhere;
      const plural = S.kind.kind + (S.kind.kind.endsWith("s") ? "" : "s");
      return { key: `list|${every ? "*" : S.ctx}|${S.kind.id}`, title: `${plural} - ${every ? "all clusters" : c}`,
        ctx: S.ctx, ns: S.ns, view: "list", kind: S.kind.id, every };
    }
    return null;
  }

  // Called after any navigation: select the matching tab, or add one.
  function note() {
    if (restoring) return;
    const t = current();
    if (!t) return;
    const i = tabs.findIndex((x) => x.key === t.key);
    if (i >= 0) { tabs[i] = { ...tabs[i], ...t }; active = t.key; }
    else {
      // Replace the active tab when it was just a stepping stone? No: Lens keeps every
      // opened view. Cap the strip so it stays usable.
      tabs.push(t);
      if (tabs.length > MAX) tabs.splice(tabs.findIndex((x) => x.key !== t.key), 1);
      active = t.key;
    }
    render(); save();
  }

  async function open(key) {
    const t = tabs.find((x) => x.key === key);
    if (!t) return;
    active = key; render(); save();
    restoring = true;
    try {
      if (t.ctx !== S.ctx) {
        await switchContext(t.ctx, t.view === "list" ? t.kind : undefined, t.ns || "");
      } else if (S.ns !== (t.ns || "")) {
        S.ns = t.ns || ""; $("#ns").value = S.ns; store.set("ns", S.ns);
        if (t.view === "list" && S.kind && S.kind.id === t.kind) await load();
      }
      if (t.view === "map") await showMap(null, t.scope);
      else if (t.view === "overview") await showOverview(t.ns || undefined);
      else if (t.view === "helm") await showHelm(t.kind);
      else if (t.view === "list") {
        if (!S.kind || S.kind.id !== t.kind || S.overview || S.map || S.helm) await setKind(t.kind);
        if (!!t.every !== !!S.everywhere) setEverywhere(!!t.every); else load();
      }
    } finally { restoring = false; }
  }

  function close(key) {
    const i = tabs.findIndex((x) => x.key === key);
    if (i < 0) return;
    tabs.splice(i, 1);
    if (active === key) {
      const next = tabs[Math.min(i, tabs.length - 1)];
      active = next ? next.key : null;
      render(); save();
      if (next) open(next.key);
      else showEmpty(); // the last tab: an empty page, not the view again
      return;
    }
    render(); save();
  }

  function render() {
    const s = strip();
    if (!s) return;
    s.hidden = tabs.length === 0;
    s.replaceChildren(...tabs.map((t) => el("div", {
        class: "vtab" + (t.key === active ? " on" : ""), role: "tab", "aria-selected": String(t.key === active), title: t.title,
        style: `--h:${ctxHue(t.ctx)}`,
        onclick: () => { if (t.key !== active) open(t.key); },
        onauxclick: (e) => { if (e.button === 1) { e.preventDefault(); close(t.key); } },
      },
      el("span", { class: "vtab-dot" }),
      el("span", { class: "vtab-label", text: t.title }),
      el("button", { type: "button", class: "vtab-x", title: "Close (middle-click works too)", "aria-label": "Close " + t.title,
        onclick: (e) => { e.stopPropagation(); close(t.key); } }, "✕"))));
    const on = s.querySelector(".vtab.on");
    if (on) on.scrollIntoView({ block: "nearest", inline: "nearest" });
  }

  // A cluster removed from the kubeconfig takes its tabs with it.
  function prune(contexts) {
    const names = new Set(contexts.map((c) => c.name));
    const before = tabs.length;
    tabs = tabs.filter((t) => names.has(t.ctx));
    if (tabs.length !== before) { if (!tabs.some((t) => t.key === active)) active = null; render(); save(); }
  }

  load();
  return { note, close, open, render, prune, closeActive: () => active && close(active), count: () => tabs.length };
})();
