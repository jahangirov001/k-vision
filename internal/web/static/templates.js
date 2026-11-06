// Starting points for "+ Create". The templates live in Go (internal/kube/templates.go)
// so the terminal's :create and this editor offer the same ones; they are fetched
// once from /api/templates. NAMESPACE is replaced with the namespace in view.
"use strict";
(function (root) {
  let T = {}, MENU = [];

  // A skeleton for any other kind the cluster serves, from its API group and version.
  function generic(kind) {
    const api = kind.group ? `${kind.group}/${kind.version}` : kind.version;
    return `apiVersion: ${api}
kind: ${kind.kind}
metadata:
  name: my-${kind.kind.toLowerCase()}
${kind.namespaced ? "  namespace: NAMESPACE\n" : ""}spec: {}
`;
  }

  function forKind(kind, ns) {
    const t = (kind && T[kind.id]) || (kind && kind.kind ? generic(kind) : T["deployments.apps"] || "");
    return t.replaceAll("NAMESPACE", ns || "default");
  }

  // load fills the templates from the server (call once with the API helper).
  function load(list) {
    T = {}; MENU = [];
    for (const t of list || []) { T[t.id] = t.yaml; MENU.push([t.id, t.label]); }
    api.MENU = MENU;
  }

  const api = { forKind, load, MENU, has: (id) => id in T };
  if (typeof module !== "undefined") module.exports = api;
  else root.KCTemplates = api;
})(typeof window !== "undefined" ? window : globalThis);
