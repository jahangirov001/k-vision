// Log line colouring, shared by the drawer and the dock. Turns one line of text into
// DOM nodes (never HTML strings: log text is untrusted) with a class per part:
//   level badge   ERROR / WARN / INFO / DEBUG / TRACE
//   timestamps    inside the message (2026-11-02T15:26:26Z, 15:26:26.417)
//   key=value     logfmt keys and values, and the keys of JSON lines
//   strings, numbers, booleans, URLs, durations, IPs, HTTP status codes
// and reports the line's level, so the row can be tinted (red for errors).
"use strict";
(function (root) {
  const LEVELS = {
    fatal: "err", panic: "err", crit: "err", critical: "err", error: "err", err: "err", e: "err", alert: "err", emerg: "err",
    warning: "warn", warn: "warn", w: "warn",
    info: "info", notice: "info", i: "info",
    debug: "debug", dbg: "debug", d: "debug", trace: "debug", verbose: "debug",
  };
  const LABEL = { err: "ERROR", warn: "WARN", info: "INFO", debug: "DEBUG" };

  // Tokens inside a message, in priority order.
  const TOKEN = new RegExp([
    /(?<url>https?:\/\/[^\s"'<>]+)/.source,
    /(?<ts>\b\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(?:[.,]\d+)?(?:Z|[+-]\d\d:?\d\d)?\b|\b\d\d:\d\d:\d\d(?:\.\d+)?\b)/.source,
    /(?<str>"(?:[^"\\]|\\.)*")/.source,
    /(?<ip>\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b)/.source,
    /(?<dur>\b\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)\b)/.source,
    /(?<num>\b-?\d+(?:\.\d+)?\b)/.source,
    /(?<bool>\b(?:true|false|null|nil|None)\b)/.source,
    /(?<lvl>\b(?:FATAL|PANIC|ERROR|ERR|WARN(?:ING)?|INFO|DEBUG|TRACE)\b)/.source,
  ].join("|"), "g");
  const KV = /(?<k>[A-Za-z_][\w.\-/]*)=(?<v>"(?:[^"\\]|\\.)*"|[^\s"]*)/g;

  const span = (cls, text) => { const s = document.createElement("span"); s.className = cls; s.textContent = text; return s; };

  // Append `text` to `out`, colouring tokens; `find` (lowercase) is highlighted.
  function tokens(out, text, find) {
    // Only an access-log-looking line gets 2xx/4xx/5xx colours, or every PID and
    // byte count in the 200s would be painted as an HTTP status.
    const httpish = /\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|HTTP\/\d|status[=:]|code[=:])/i.test(text);
    let last = 0;
    TOKEN.lastIndex = 0;
    for (let m; (m = TOKEN.exec(text)); ) {
      if (m.index > last) plain(out, text.slice(last, m.index), find);
      const g = m.groups;
      const cls = g.url ? "lg-url" : g.ts ? "lg-ts" : g.str ? "lg-str" : g.ip ? "lg-num" : g.dur ? "lg-num"
        : g.num ? (httpish && /^[45]\d\d$/.test(m[0]) ? "lg-code-bad" : httpish && /^[23]\d\d$/.test(m[0]) ? "lg-code-ok" : "lg-num")
        : g.bool ? "lg-bool" : "lg-lvlw-" + (LEVELS[m[0].toLowerCase()] || "info");
      const s = span(cls, "");
      plain(s, m[0], find);
      out.append(s);
      last = m.index + m[0].length;
    }
    if (last < text.length) plain(out, text.slice(last), find);
  }

  function plain(out, text, find) {
    if (!find) { out.append(text); return; }
    const low = text.toLowerCase();
    let i = 0, j;
    while ((j = low.indexOf(find, i)) >= 0) {
      out.append(text.slice(i, j), span("lg-mark", text.slice(j, j + find.length)));
      i = j + find.length;
    }
    out.append(text.slice(i));
  }

  function levelOf(v) { return LEVELS[String(v || "").toLowerCase().replace(/[^a-z]/g, "")] || ""; }

  // JSON log lines: {"level":"info","ts":...,"msg":"...","k":v}
  function fromJSON(text, find) {
    let o;
    try { o = JSON.parse(text); } catch (_) { return null; }
    if (!o || typeof o !== "object" || Array.isArray(o)) return null;
    const lvlKey = ["level", "lvl", "severity", "log.level", "levelname"].find((k) => k in o);
    const msgKey = ["msg", "message", "event", "log"].find((k) => k in o && typeof o[k] === "string");
    const tsKey = ["ts", "time", "timestamp", "@timestamp", "t"].find((k) => k in o);
    const level = lvlKey ? levelOf(o[lvlKey]) : "";
    const frag = document.createDocumentFragment();
    if (tsKey) frag.append(span("lg-ts", String(o[tsKey])), " ");
    if (msgKey) { const m = span("lg-msg", ""); tokens(m, o[msgKey], find); frag.append(m); }
    for (const [k, v] of Object.entries(o)) {
      if (k === lvlKey || k === msgKey || k === tsKey) continue;
      frag.append(" ", span("lg-key", k), span("lg-eq", "="));
      const val = typeof v === "string" ? v : JSON.stringify(v);
      const s = span(typeof v === "number" ? "lg-num" : typeof v === "boolean" || v === null ? "lg-bool" : typeof v === "object" ? "lg-obj" : "lg-val", "");
      if (/^(err|error|exception|stack|stacktrace)$/i.test(k)) s.className = "lg-errval";
      plain(s, val, find);
      frag.append(s);
    }
    return { frag, level };
  }

  // logfmt and plain text: level= / LEVEL word / [E] prefixes, then key=value pairs.
  function fromText(text, find) {
    let level = "";
    const kvLevel = text.match(/\b(?:level|lvl|severity)=["']?([A-Za-z]+)/);
    if (kvLevel) level = levelOf(kvLevel[1]);
    if (!level) {
      const w = text.match(/^\s*(?:\[?\s*)?(FATAL|PANIC|ERROR|ERR|WARN(?:ING)?|INFO|DEBUG|TRACE)\b|\b(FATAL|PANIC|ERROR|WARN(?:ING)?)\b/);
      if (w) level = levelOf(w[1] || w[2]);
    }
    if (!level) { const k = text.match(/^([EWID])\d{4} /); if (k) level = levelOf(k[1]); } // klog: E1102 15:55:28…
    if (!level && /\b(exception|traceback|panic:|fatal error|failed|refused|timeout|timed out)\b/i.test(text)) level = "err";
    const frag = document.createDocumentFragment();
    let last = 0;
    KV.lastIndex = 0;
    let any = false;
    for (let m; (m = KV.exec(text)); ) {
      any = true;
      if (m.index > last) tokens(frag, text.slice(last, m.index), find);
      const { k, v } = m.groups;
      if (/^(level|lvl|severity)$/i.test(k)) { last = m.index + m[0].length; continue; } // shown as the badge
      frag.append(span("lg-key", k), span("lg-eq", "="));
      const isMsg = /^(msg|message)$/i.test(k), isErr = /^(err|error)$/i.test(k);
      const s = span(isErr ? "lg-errval" : isMsg ? "lg-msg" : "lg-val", "");
      if (isMsg || isErr) plain(s, v, find); else tokens(s, v, find);
      frag.append(s);
      last = m.index + m[0].length;
    }
    if (!any) { tokens(frag, text, find); return { frag, level }; }
    if (last < text.length) tokens(frag, text.slice(last), find);
    return { frag, level };
  }

  // render(text, find) -> { node, level } ; level is "err" | "warn" | "info" | "debug" | ""
  function render(text, find) {
    find = (find || "").toLowerCase();
    const t = text.trimStart();
    const r = (t.startsWith("{") && fromJSON(t, find)) || fromText(text, find);
    const node = document.createElement("span");
    node.className = "lg-body";
    if (r.level) node.append(span("lg-lvl lg-lvl-" + r.level, LABEL[r.level]), " ");
    node.append(r.frag);
    return { node, level: r.level };
  }

  const api = { render, levelOf };
  if (typeof module !== "undefined") module.exports = api;
  else root.KCLog = api;
})(typeof window !== "undefined" ? window : globalThis);
