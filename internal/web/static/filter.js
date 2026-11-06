// The filter language, browser side. Same grammar and semantics as
// internal/kube/filter.go -- both are tested against
// internal/kube/testdata/filter_cases.json, so change them together.
"use strict";
(function (root) {
  const HEALTH = {
    healthy: ["ok"], ok: ["ok"], warning: ["warn"], warn: ["warn"],
    failing: ["bad"], bad: ["bad"], error: ["bad"], failed: ["bad"],
    unhealthy: ["warn", "bad"], problem: ["warn", "bad"],
  };
  const UNITS = { s: 1, m: 60, h: 3600, d: 86400, w: 604800 };
  const OP = /^([a-z0-9][a-z0-9._/-]*)(>=|<=|!=|>|<|=)(.*)$/;
  const DUR = /^(\d+(?:\.\d+)?)(s|m|h|d|w)$/;
  const NUM = /^-?\d+(?:\.\d+)?/;

  function tokenize(s) {
    const out = []; let cur = "", q = false, has = false;
    for (const ch of s || "") {
      if (ch === '"') { q = !q; has = true; }
      else if (!q && /\s/.test(ch)) { if (has) { out.push(cur); cur = ""; has = false; } }
      else { cur += ch; has = true; }
    }
    if (has) out.push(cur);
    return out;
  }
  const alts = (v) => { const a = v.split("|").filter(Boolean); return a.length ? a : [""]; };

  function parseTerm(tok) {
    const t = { neg: false };
    if (tok.length > 1 && tok[0] === "-") { t.neg = true; tok = tok.slice(1); }
    const low = tok.toLowerCase();
    t.raw = low;
    if (low === "!ok") return { ...t, kind: "health", health: HEALTH.unhealthy };
    if (low.startsWith("is:") && HEALTH[low.slice(3)]) return { ...t, kind: "health", health: HEALTH[low.slice(3)] };
    const m = low.match(OP);
    if (m) {
      const [, key, op, val] = m;
      if (op === "=" || op === "!=") return { ...t, kind: "label", key, op, alts: alts(val) };
      if (key === "age") {
        const d = val.match(DUR);
        if (d) return { ...t, kind: "age", op, secs: parseFloat(d[1]) * UNITS[d[2]] };
      } else if (val !== "" && !isNaN(Number(val))) {
        return { ...t, kind: "cmp", key, op, num: Number(val) };
      }
    }
    const i = low.indexOf(":");
    if (i > 0) {
      const key = low.slice(0, i), rest = low.slice(i + 1);
      if (key === "label" || key === "l") {
        let [k, v = ""] = rest.split(/=(.*)/s);
        let op = "=";
        if (k.endsWith("!")) { k = k.slice(0, -1); op = "!="; }
        return { ...t, kind: "label", key: k, op, alts: alts(v) };
      }
      return { ...t, kind: "field", key, alts: alts(rest) };
    }
    return { ...t, kind: "text", alts: alts(low) };
  }

  const cmp = (x, op, y) => op === ">" ? x > y : op === "<" ? x < y : op === ">=" ? x >= y : op === "<=" ? x <= y : false;

  function field(r, key) {
    switch (key) {
      case "name": return r.name;
      case "ns": case "namespace": return r.namespace;
      case "status": case "st": return r.status;
      case "ctx": case "context": case "cluster": return r.context || "";
      case "age": return r.age;
    }
    for (const [k, v] of Object.entries(r.cells || {})) if (k.toLowerCase() === key) return v;
    return undefined;
  }
  function label(labels, key) {
    for (const [k, v] of Object.entries(labels || {})) if (k.toLowerCase() === key) return [String(v), true];
    return ["", false];
  }
  function hay(r) {
    const cells = Object.keys(r.cells || {}).sort().map((k) => r.cells[k]);
    return [`${r.namespace}/${r.name}`, r.status, r.context || "", ...cells].join(" ").toLowerCase();
  }
  const ageSecs = (r) => {
    const t = new Date(r.created).getTime();
    return isNaN(t) || t <= 0 ? null : (Date.now() - t) / 1000;
  };

  function termMatch(t, r) {
    switch (t.kind) {
      case "health": return t.health.includes(r.health);
      case "age": { const a = ageSecs(r); return a != null && cmp(a, t.op, t.secs); }
      case "cmp": {
        const v = field(r, t.key);
        if (v == null) return false;
        const n = String(v).trim().match(NUM);
        return !!n && cmp(parseFloat(n[0]), t.op, t.num);
      }
      case "label": {
        const [v, has] = label(r.labels, t.key);
        let eq = has;
        if (!(t.alts.length === 1 && t.alts[0] === "")) eq = has && t.alts.includes(v.toLowerCase());
        return t.op === "!=" ? !eq : eq;
      }
      case "field": {
        const v = field(r, t.key);
        if (v == null) return hay(r).includes(t.raw);
        return t.alts.some((a) => String(v).toLowerCase().includes(a));
      }
    }
    const h = hay(r);
    return t.alts.some((a) => h.includes(a));
  }

  function parse(s) {
    const terms = tokenize(s).map(parseTerm);
    return { empty: terms.length === 0, terms, match: (r) => terms.every((t) => termMatch(t, r) !== t.neg) };
  }

  // Chips: the parsed filter shown back to the person, so "restarts>3" visibly became
  // a numeric compare rather than a text search.
  function describe(s) {
    return tokenize(s).map((tok) => {
      const t = parseTerm(tok);
      const kinds = { health: "health", age: "age", cmp: "compare", label: "label", field: "field", text: "text" };
      return { text: tok, kind: kinds[t.kind], neg: t.neg };
    });
  }

  const api = { parse, describe, tokenize };
  if (typeof module !== "undefined") module.exports = api;
  else root.KCFilter = api;
})(typeof window !== "undefined" ? window : globalThis);
