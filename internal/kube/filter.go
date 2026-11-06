package kube

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Filter is the query language both UIs share. A filter is whitespace-separated
// terms, all of which must match (AND):
//
//	repo                  text: namespace/name, status, context or any column contains it
//	"nightly report"      quotes keep spaces in one term
//	-repo                 a leading "-" negates any term
//	a|b                   alternatives inside a term: status:crash|error
//	is:failing            health: healthy|ok, warning|warn, failing|bad|error, unhealthy
//	!ok                   short for is:unhealthy
//	ns:redcore            field contains: name, ns, status, ctx, or any column (node, image, sync…)
//	restarts>3            numeric compare on a column: > < >= <=
//	age<1h  age>7d        age compare, units s m h d w
//	app=web  tier!=db     label equals / differs (app= : label exists)
//	label:team            label exists; label:team=core equals
//
// Anything that does not parse as one of these is matched as text, so a URL or a
// typo narrows the list rather than erroring. internal/web/static/filter.js is the
// same language for the browser; testdata/filter_cases.json is shared by both test
// suites so they cannot drift apart.
type Filter struct{ terms []term }

type termKind int

const (
	tText termKind = iota
	tHealth
	tField
	tLabel
	tCompare
	tAge
)

type term struct {
	kind   termKind
	neg    bool
	raw    string // lowercased, without the leading "-"
	key    string
	op     string
	alts   []string
	num    float64
	dur    time.Duration
	health []Health
}

var (
	opRe  = regexp.MustCompile(`^([a-z0-9][a-z0-9._/-]*)(>=|<=|!=|>|<|=)(.*)$`)
	durRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)(s|m|h|d|w)$`)
	numRe = regexp.MustCompile(`^-?\d+(?:\.\d+)?`)
)

var healthWords = map[string][]Health{
	"healthy": {Healthy}, "ok": {Healthy},
	"warning": {Warning}, "warn": {Warning},
	"failing": {Failing}, "bad": {Failing}, "error": {Failing}, "failed": {Failing},
	"unhealthy": {Warning, Failing}, "problem": {Warning, Failing},
}

var durUnits = map[string]time.Duration{
	"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

// ParseFilter parses once; Match is then cheap per row.
func ParseFilter(s string) Filter {
	var f Filter
	for _, tok := range tokenize(s) {
		f.terms = append(f.terms, parseTerm(tok))
	}
	return f
}

func (f Filter) Empty() bool { return len(f.terms) == 0 }

// Matches is ParseFilter(filter).Match without a context.
func Matches(r Row, filter string) bool { return ParseFilter(filter).Match("", r) }

// Match reports whether a row from `context` passes every term.
func (f Filter) Match(context string, r Row) bool {
	for _, t := range f.terms {
		if t.match(context, r) == t.neg {
			return false
		}
	}
	return true
}

func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote, has = !inQuote, true
		case !inQuote && (r == ' ' || r == '\t' || r == '\n'):
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

func parseTerm(tok string) term {
	var t term
	if len(tok) > 1 && tok[0] == '-' {
		t.neg, tok = true, tok[1:]
	}
	low := strings.ToLower(tok)
	t.raw = low
	if low == "!ok" {
		t.kind, t.health = tHealth, healthWords["unhealthy"]
		return t
	}
	if v, ok := strings.CutPrefix(low, "is:"); ok {
		if h, ok := healthWords[v]; ok {
			t.kind, t.health = tHealth, h
			return t
		}
	}
	if m := opRe.FindStringSubmatch(low); m != nil {
		key, op, val := m[1], m[2], m[3]
		switch op {
		case "=", "!=":
			t.kind, t.key, t.op, t.alts = tLabel, key, op, splitAlts(val)
			return t
		default:
			if key == "age" {
				if d, ok := parseDur(val); ok {
					t.kind, t.op, t.dur = tAge, op, d
					return t
				}
			} else if n, err := strconv.ParseFloat(val, 64); err == nil {
				t.kind, t.key, t.op, t.num = tCompare, key, op, n
				return t
			}
		}
	}
	if i := strings.IndexByte(low, ':'); i > 0 {
		key, rest := low[:i], low[i+1:]
		if key == "label" || key == "l" {
			k, v, _ := strings.Cut(rest, "=")
			t.kind, t.op = tLabel, "="
			if strings.HasSuffix(k, "!") {
				k, t.op = strings.TrimSuffix(k, "!"), "!="
			}
			t.key, t.alts = k, splitAlts(v)
			return t
		}
		t.kind, t.key, t.alts = tField, key, splitAlts(rest)
		return t
	}
	t.kind, t.alts = tText, splitAlts(low)
	return t
}

func splitAlts(v string) []string {
	var out []string
	for _, a := range strings.Split(v, "|") {
		if a != "" {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func parseDur(s string) (time.Duration, bool) {
	m := durRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	return time.Duration(n * float64(durUnits[m[2]])), true
}

func (t term) match(ctx string, r Row) bool {
	switch t.kind {
	case tHealth:
		for _, h := range t.health {
			if r.Health == h {
				return true
			}
		}
		return false
	case tAge:
		if r.Created.IsZero() {
			return false
		}
		return compare(time.Since(r.Created).Seconds(), t.op, t.dur.Seconds())
	case tCompare:
		v, ok := fieldValue(ctx, r, t.key)
		if !ok {
			return false
		}
		n := numRe.FindString(strings.TrimSpace(v))
		if n == "" {
			return false
		}
		x, _ := strconv.ParseFloat(n, 64)
		return compare(x, t.op, t.num)
	case tLabel:
		v, has := labelValue(r.Labels, t.key)
		eq := has
		if !(len(t.alts) == 1 && t.alts[0] == "") {
			eq = has && anyEqual(strings.ToLower(v), t.alts)
		}
		if t.op == "!=" {
			return !eq
		}
		return eq
	case tField:
		v, ok := fieldValue(ctx, r, t.key)
		if !ok {
			return strings.Contains(haystack(ctx, r), t.raw)
		}
		return containsAny(strings.ToLower(v), t.alts)
	}
	return containsAny(haystack(ctx, r), t.alts)
}

func fieldValue(ctx string, r Row, key string) (string, bool) {
	switch key {
	case "name":
		return r.Name, true
	case "ns", "namespace":
		return r.Namespace, true
	case "status", "st":
		return r.Status, true
	case "ctx", "context", "cluster":
		return ctx, true
	case "age":
		return r.Age, true
	}
	for k, v := range r.Cells {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func labelValue(labels map[string]string, key string) (string, bool) {
	for k, v := range labels {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func haystack(ctx string, r Row) string {
	parts := []string{r.Namespace + "/" + r.Name, r.Status, ctx}
	keys := make([]string, 0, len(r.Cells))
	for k := range r.Cells {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, r.Cells[k])
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func containsAny(s string, alts []string) bool {
	for _, a := range alts {
		if strings.Contains(s, a) {
			return true
		}
	}
	return false
}

func anyEqual(s string, alts []string) bool {
	for _, a := range alts {
		if s == a {
			return true
		}
	}
	return false
}

func compare(x float64, op string, y float64) bool {
	switch op {
	case ">":
		return x > y
	case "<":
		return x < y
	case ">=":
		return x >= y
	case "<=":
		return x <= y
	}
	return false
}
