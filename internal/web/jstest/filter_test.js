// node internal/web/jstest/filter_test.js -- the browser filter against the shared cases.
"use strict";
const path = require("path");
const fs = require("fs");
const F = require(path.join(__dirname, "..", "static", "filter.js"));
const fx = JSON.parse(fs.readFileSync(path.join(__dirname, "..", "..", "kube", "testdata", "filter_cases.json"), "utf8"));
const rows = fx.rows.map((r) => ({ ...r, created: new Date(Date.now() - r.ageSeconds * 1000).toISOString() }));
let failed = 0;
for (const c of fx.cases) {
  const f = F.parse(c.filter);
  const got = rows.filter((r) => f.match(r)).map((r) => r.id);
  if (JSON.stringify(got) !== JSON.stringify(c.match)) {
    failed++;
    console.log(`FAIL filter ${JSON.stringify(c.filter)}: got ${JSON.stringify(got)}, want ${JSON.stringify(c.match)}`);
  }
}
console.log(`${fx.cases.length - failed}/${fx.cases.length} filter cases pass (js)`);
process.exit(failed ? 1 : 0);
