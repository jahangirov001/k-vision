package tui

// The terminal versions of what the desktop window has beyond lists: Helm releases
// and charts, the cluster and namespace topology, creating resources from YAML,
// copying files to and from pods and nodes, merged logs of several pods, and log
// colouring. Same internal/kube calls as the window, same confirmations.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/njahangirov/kube-console/internal/kube"
)

// ---------------------------------------------------------------- helm

func (m *Model) helmCmd(args []string) tea.Cmd {
	if m.cluster == nil {
		return nil
	}
	c, ns := m.cluster, m.namespace
	sub := "releases"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "releases", "list", "ls":
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			rs, err := c.HelmReleases(ctx, ns)
			if err != nil {
				return textMsg{err: err}
			}
			return textMsg{title: fmt.Sprintf("Helm releases (%d)  ·  :helm <name> for values, history, manifest", len(rs)), body: helmReleasesText(rs)}
		}
	case "charts":
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cs, err := c.HelmCharts(ctx, ns)
			if err != nil {
				return textMsg{err: err}
			}
			return textMsg{title: fmt.Sprintf("Helm charts (%d)", len(cs)), body: helmChartsText(cs)}
		}
	}
	// :helm <release> [values|all|manifest|notes|history] [revision]
	name, part, rev := args[0], "summary", 0
	for _, a := range args[1:] {
		if n, err := strconv.Atoi(a); err == nil {
			rev = n
		} else {
			part = strings.ToLower(a)
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		rns := ns
		if i := strings.Index(name, "/"); i > 0 {
			rns, name = name[:i], name[i+1:]
		}
		if rns == "" {
			rs, err := c.HelmReleases(ctx, "")
			if err != nil {
				return textMsg{err: err}
			}
			var hits []string
			for _, r := range rs {
				if r.Name == name {
					hits = append(hits, r.Namespace)
				}
			}
			switch len(hits) {
			case 0:
				return textMsg{err: fmt.Errorf("no Helm release named %q (:helm lists them)", name)}
			case 1:
				rns = hits[0]
			default:
				return textMsg{err: fmt.Errorf("%q is in %d namespaces (%s); use :helm <namespace>/%s", name, len(hits), strings.Join(hits, ", "), name)}
			}
		}
		d, err := c.HelmRelease(ctx, rns, name, rev)
		if err != nil {
			return textMsg{err: err}
		}
		return textMsg{title: fmt.Sprintf("Helm %s/%s  rev %d  ·  %s", d.Namespace, d.Name, d.Revision, part), body: helmDetailText(d, part)}
	}
}

func helmReleasesText(rs []kube.HelmRelease) string {
	if len(rs) == 0 {
		return stDim.Render("No Helm releases here.")
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Namespace+rs[i].Name < rs[j].Namespace+rs[j].Name })
	rows := [][]string{{"NAMESPACE", "NAME", "REV", "STATUS", "CHART", "APP VERSION", "UPDATED"}}
	for _, r := range rs {
		rows = append(rows, []string{r.Namespace, r.Name, strconv.Itoa(r.Revision), statusCell(r.Health, r.Status),
			r.Chart + "-" + r.ChartVersion, r.AppVersion, ago(r.Updated)})
	}
	return table(rows)
}

func helmChartsText(cs []kube.HelmChart) string {
	if len(cs) == 0 {
		return stDim.Render("No Helm charts deployed here.")
	}
	rows := [][]string{{"CHART", "VERSIONS", "RELEASES"}}
	for _, c := range cs {
		v := strings.Join(c.Versions, ", ")
		if len(c.Versions) > 1 {
			v = stWarn.Render(v)
		}
		rel := strings.Join(c.Releases, ", ")
		if len(c.Releases) > 4 {
			rel = strings.Join(c.Releases[:4], ", ") + fmt.Sprintf(" +%d", len(c.Releases)-4)
		}
		rows = append(rows, []string{c.Name, v, rel})
	}
	return table(rows) + "\n" + stDim.Render("  amber: more than one version of the chart is deployed")
}

func helmDetailText(d *kube.HelmDetail, part string) string {
	var b strings.Builder
	switch part {
	case "values":
		b.WriteString(orNone(d.Values, "No values were set; the chart's defaults apply (:helm "+d.Name+" all)."))
	case "all", "allvalues", "computed":
		b.WriteString(d.AllValues)
	case "manifest":
		b.WriteString(d.Manifest)
	case "notes":
		b.WriteString(orNone(d.Notes, "This chart has no notes."))
	case "history":
		rows := [][]string{{"REV", "STATUS", "CHART", "APP VERSION", "UPDATED", "DESCRIPTION"}}
		for _, h := range d.History {
			rows = append(rows, []string{strconv.Itoa(h.Revision), statusCell(h.Health, h.Status), h.Chart + "-" + h.ChartVersion, h.AppVersion, ago(h.Updated), h.Description})
		}
		b.WriteString(table(rows))
	default:
		fmt.Fprintf(&b, "%s  %s\n", stHeader.Render("STATUS  "), statusCell(d.Health, d.Status))
		fmt.Fprintf(&b, "%s  %s-%s  (app %s)\n", stHeader.Render("CHART   "), d.Chart, d.ChartVersion, d.AppVersion)
		fmt.Fprintf(&b, "%s  %d of %d · %s\n", stHeader.Render("REVISION"), d.Revision, len(d.History), ago(d.Updated))
		if d.ChartSource != "" {
			fmt.Fprintf(&b, "%s  %s\n", stHeader.Render("SOURCE  "), d.ChartSource)
		}
		if d.Description != "" {
			fmt.Fprintf(&b, "%s  %s\n", stHeader.Render("LAST    "), d.Description)
		}
		b.WriteString("\n" + stSection.Render("VALUES") + stDim.Render("  (set by the release)") + "\n")
		b.WriteString(orNone(d.Values, "none; the chart's defaults apply") + "\n")
		b.WriteString(stDim.Render(fmt.Sprintf("\nMore: :helm %s/%s values | all | manifest | notes | history  [revision]", d.Namespace, d.Name)))
	}
	return b.String()
}

func orNone(s, none string) string {
	if strings.TrimSpace(s) == "" || strings.TrimSpace(s) == "{}" {
		return stDim.Render(none)
	}
	return s
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// table lays rows out in columns; the first row is the header.
func table(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	w := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(w) {
				w[i] = max(w[i], len([]rune(stripANSI(c))))
			}
		}
	}
	var b strings.Builder
	for n, r := range rows {
		b.WriteString(" ")
		for i, c := range r {
			if i >= len(w) {
				break
			}
			if n == 0 {
				c = stHeader.Render(c)
			}
			b.WriteString(padTo(c, w[i]))
			if i < len(r)-1 {
				b.WriteString("  ")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ---------------------------------------------------------------- topology

func (m *Model) topologyCmd(args []string) tea.Cmd {
	if m.cluster == nil {
		return nil
	}
	c := m.cluster
	ns := ""
	system := false
	for _, a := range args {
		switch a {
		case "helm":
			hns := m.namespace
			m.status, m.isErr = "reading Helm releases…", false
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				hm, err := c.HelmMap(ctx, hns)
				if err != nil {
					return textMsg{err: err}
				}
				return textMsg{title: "Helm Topology · " + short(c.Context) + " · " + orAllNS(hns), body: helmTopologyText(hm)}
			}
		case "cluster":
			ns = ""
		case "+system", "-a", "all":
			system = true
		default:
			ns = a
		}
	}
	if ns != "" {
		m.status, m.isErr = "mapping "+ns+"…", false
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			g, err := c.Graph(ctx, ns)
			if err != nil {
				return textMsg{err: err}
			}
			return textMsg{title: "Namespace Topology · " + ns, body: namespaceTopologyText(g)}
		}
	}
	m.status, m.isErr = "mapping the cluster…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cm, err := c.ClusterMap(ctx)
		if err != nil {
			return textMsg{err: err}
		}
		return textMsg{title: "Cluster Topology · " + short(c.Context), body: clusterTopologyText(cm, system)}
	}
}

func pct(a, b int64) int {
	if b == 0 {
		return 0
	}
	return int(a * 100 / b)
}

// bar draws a percentage as ▰▱, coloured by how full it is.
func bar(p, width int) string {
	n := min(width, max(0, p*width/100))
	s := strings.Repeat("▰", n)
	st := stCmd
	switch {
	case p >= 90:
		st = stBad
	case p >= 70:
		st = stWarn
	}
	return st.Render(s) + stFaint.Render(strings.Repeat("▱", width-n))
}

func healthDot(h kube.Health) string {
	switch h {
	case kube.Failing:
		return stBad.Render("●")
	case kube.Warning:
		return stWarn.Render("●")
	case kube.Healthy:
		return stOK.Render("●")
	}
	return stFaint.Render("○")
}

func clusterTopologyText(cm *kube.ClusterMap, system bool) string {
	var b strings.Builder
	var cpuR, cpuA, memR, memA int64
	for _, p := range cm.Pools {
		cpuR, cpuA, memR, memA = cpuR+p.CPUReq, cpuA+p.CPUAlloc, memR+p.MemReq, memA+p.MemAlloc
	}
	bad := 0
	for _, n := range cm.Namespaces {
		if n.Health == kube.Failing || n.Health == kube.Warning {
			bad++
		}
	}
	fmt.Fprintf(&b, " %s %d   %s %d   %s %d   %s %d%%   %s %d%%   %s %d\n\n",
		stHeader.Render("NODES"), cm.Nodes, stHeader.Render("PODS"), cm.Pods, stHeader.Render("NAMESPACES"), len(cm.Namespaces),
		stHeader.Render("CPU REQ"), pct(cpuR, cpuA), stHeader.Render("MEM REQ"), pct(memR, memA), stHeader.Render("NEED A LOOK"), bad)

	b.WriteString(stSection.Render(" NODE POOLS") + stDim.Render("   each square is a node: ") +
		stOK.Render("■") + stDim.Render(" <60% CPU requested  ") + stWarn.Render("■") + stDim.Render(" 60–90%  ") +
		stBad.Render("■") + stDim.Render(" ≥90% / not ready   ◇ spot") + "\n")
	for _, p := range cm.Pools {
		src := map[string]string{"karpenter": "karpenter", "eks": "node group"}[p.Source]
		fmt.Fprintf(&b, "\n %s %s %s\n", healthDot(p.Health), stKey.Render(p.Name), stDim.Render(strings.TrimSuffix(fmt.Sprintf("%s · %d node%s · %d pods · %s", orDash(src), len(p.Nodes), plural(len(p.Nodes)), p.Pods, strings.Join(p.Types, ", ")), " · ")))
		fmt.Fprintf(&b, "   CPU    %s %3d%%  %s\n", bar(pct(p.CPUReq, p.CPUAlloc), 24), pct(p.CPUReq, p.CPUAlloc), stDim.Render(fmt.Sprintf("%.1f / %.1f cores", float64(p.CPUReq)/1000, float64(p.CPUAlloc)/1000)))
		fmt.Fprintf(&b, "   Memory %s %3d%%  %s\n", bar(pct(p.MemReq, p.MemAlloc), 24), pct(p.MemReq, p.MemAlloc), stDim.Render(gib(p.MemReq)+" / "+gib(p.MemAlloc)+" GiB"))
		var sq strings.Builder
		for i, n := range p.Nodes {
			if i > 0 && i%40 == 0 {
				sq.WriteString("\n          ")
			}
			ch := "■"
			if n.Spot {
				ch = "◆"
			}
			switch {
			case n.Health != kube.Healthy:
				sq.WriteString(stBad.Render(ch))
			case n.CPUPct >= 90:
				sq.WriteString(stBad.Render(ch))
			case n.CPUPct >= 60:
				sq.WriteString(stWarn.Render(ch))
			default:
				sq.WriteString(stOK.Render(ch))
			}
		}
		b.WriteString("   Nodes  " + sq.String() + "\n")
		for _, n := range p.Nodes {
			if n.Health != kube.Healthy {
				fmt.Fprintf(&b, "          %s %s %s\n", stBad.Render("✕"), n.Name, stBad.Render(n.Status))
			}
		}
	}

	pods := map[string]map[string]int{}
	for _, l := range cm.Links {
		if pods[l.Namespace] == nil {
			pods[l.Namespace] = map[string]int{}
		}
		pods[l.Namespace][l.Pool] += l.Pods
	}
	hidden := 0
	b.WriteString("\n" + stSection.Render(" NAMESPACES") + stDim.Render("   → node pools their pods run on    :topology <namespace> for one") + "\n\n")
	rows := [][]string{{"", "NAMESPACE", "STATUS", "WORKLOADS", "PODS", "SVC", "ING", "CPU REQ", "RUNS ON"}}
	for _, n := range cm.Namespaces {
		if n.System && !system {
			hidden++
			continue
		}
		st := "healthy"
		switch {
		case n.Failing > 0:
			st = stBad.Render(fmt.Sprintf("%d failing", n.Failing))
		case n.Warning > 0:
			st = stWarn.Render(fmt.Sprintf("%d not ready", n.Warning))
		case n.Pods == 0:
			st = stDim.Render("no pods")
		default:
			st = stOK.Render(st)
		}
		var on []string
		for pool, c := range pods[n.Name] {
			on = append(on, fmt.Sprintf("%s ×%d", pool, c))
		}
		sort.Strings(on)
		name := n.Name
		if n.System {
			name += stFaint.Render(" (platform)")
		}
		rows = append(rows, []string{healthDot(n.Health), name, st, strconv.Itoa(n.Workloads), strconv.Itoa(n.Pods),
			strconv.Itoa(n.Services), strconv.Itoa(n.Ingresses), fmt.Sprintf("%.2f", float64(n.CPUReq)/1000), strings.Join(on, ", ")})
	}
	b.WriteString(table(rows))
	if hidden > 0 {
		b.WriteString("\n" + stDim.Render(fmt.Sprintf(" %d platform namespaces hidden (kube-system, monitoring…): :topology +system shows them", hidden)) + "\n")
	}
	if len(cm.Warnings) > 0 {
		b.WriteString("\n" + stWarn.Render(" not shown: "+strings.Join(cm.Warnings, "; ")) + "\n")
	}
	return b.String()
}

// namespaceTopologyText draws the namespace graph as a tree, from the traffic in:
// Ingress → Service → workload → config, then anything not reached that way.
func namespaceTopologyText(g *kube.Graph) string {
	if len(g.Nodes) == 0 {
		return stDim.Render("Nothing to map: this namespace has no workloads, services or ingresses.")
	}
	byID := map[string]*kube.GraphNode{}
	for i := range g.Nodes {
		byID[g.Nodes[i].ID] = &g.Nodes[i]
	}
	out := map[string][]string{}
	in := map[string]int{}
	for _, e := range g.Edges {
		out[e.From] = append(out[e.From], e.To)
		in[e.To]++
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool {
			a, b := byID[out[k][i]], byID[out[k][j]]
			if a == nil || b == nil {
				return out[k][i] < out[k][j]
			}
			return a.Col < b.Col || (a.Col == b.Col && a.Name < b.Name)
		})
	}
	label := func(n *kube.GraphNode) string {
		s := healthDot(n.Health) + " " + stDim.Render(n.Kind) + " " + stKey.Render(n.Name)
		if n.Status != "" {
			st := stDim
			switch n.Health {
			case kube.Failing:
				st = stBad
			case kube.Warning:
				st = stWarn
			case kube.Healthy:
				st = stOK
			}
			s += "  " + st.Render(n.Status)
		}
		if n.Info != "" {
			s += "  " + stFaint.Render(n.Info)
		}
		if len(n.Pods) > 0 {
			var d strings.Builder
			for _, p := range n.Pods {
				d.WriteString(healthDot(p.Health))
			}
			if n.PodsAll > len(n.Pods) {
				d.WriteString(stFaint.Render(fmt.Sprintf(" +%d", n.PodsAll-len(n.Pods))))
			}
			s += "  " + d.String()
		}
		for _, bd := range n.Badges {
			st := stCmd
			if bd.Health == kube.Failing {
				st = stBad
			} else if bd.Health == kube.Warning {
				st = stWarn
			}
			s += "  " + st.Render("["+bd.Text+"]")
		}
		return s
	}
	var b strings.Builder
	problems := 0
	for _, n := range g.Nodes {
		if n.Health == kube.Failing || n.Health == kube.Warning {
			problems++
		}
	}
	fmt.Fprintf(&b, " %d resources · %d to look at   %s\n\n", len(g.Nodes), problems,
		stDim.Render("● healthy/warning/failing   pods as dots   [badges] HPA · KEDA · PDB"))
	seen := map[string]bool{}
	var walk func(id, prefix string, last bool, depth int)
	walk = func(id, prefix string, last bool, depth int) {
		n := byID[id]
		if n == nil {
			return
		}
		branch, next := "├─ ", "│  "
		if last {
			branch, next = "└─ ", "   "
		}
		if depth == 0 {
			branch, next = "", ""
		}
		again := seen[id]
		line := " " + prefix + stFaint.Render(branch) + label(n)
		if again {
			line = " " + prefix + stFaint.Render(branch) + stFaint.Render("↑ "+n.Kind+" "+n.Name+" (above)")
		}
		b.WriteString(line + "\n")
		if again {
			return
		}
		seen[id] = true
		kids := out[id]
		for i, k := range kids {
			walk(k, prefix+stFaint.Render(next), i == len(kids)-1, depth+1)
		}
	}
	order := make([]*kube.GraphNode, 0, len(g.Nodes))
	for i := range g.Nodes {
		order = append(order, &g.Nodes[i])
	}
	sort.SliceStable(order, func(i, j int) bool {
		return order[i].Col < order[j].Col || (order[i].Col == order[j].Col && order[i].Name < order[j].Name)
	})
	for _, n := range order {
		if in[n.ID] == 0 && !seen[n.ID] {
			walk(n.ID, "", true, 0)
		}
	}
	for _, n := range order { // cycles or leftovers
		if !seen[n.ID] {
			walk(n.ID, "", true, 0)
		}
	}
	if len(g.Warnings) > 0 {
		b.WriteString("\n" + stWarn.Render(" not shown: "+strings.Join(g.Warnings, "; ")) + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------- create

// createCmd opens $EDITOR on a template (or a file's contents), validates the result
// with a server dry run, then creates it after confirmation.
//
//	:create                 template for the current kind
//	:create deployment      template for that kind
//	:create -f path.yaml    that file
func (m *Model) createCmd(args []string) tea.Cmd {
	if m.cluster == nil {
		return nil
	}
	ns := m.namespace
	if ns == "" {
		ns = m.cluster.Namespace
	}
	text := ""
	if len(args) >= 2 && (args[0] == "-f" || args[0] == "--file") {
		p := args[1]
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[2:])
		}
		data, err := os.ReadFile(p)
		if err != nil {
			m.setErr(err)
			return nil
		}
		return m.validateAndCreate(string(data), ns)
	}
	k := m.kind
	if len(args) > 0 {
		var err error
		if k, err = m.cluster.ResolveKind(args[0]); err != nil {
			m.setErr(err)
			return nil
		}
	}
	text = "# Edit, save and quit to validate (server dry run) and create.\n# Leave it unchanged or empty to cancel. Several objects: separate with ---\n" + kube.TemplateFor(k, ns)
	f, err := os.CreateTemp("", "kv-create-*.yaml")
	if err != nil {
		m.setErr(err)
		return nil
	}
	_, _ = f.WriteString(text)
	f.Close()
	pathName, original := f.Name(), text
	return tea.ExecProcess(editorCmd(pathName), func(err error) tea.Msg {
		return createdEditMsg{path: pathName, original: original, ns: ns, err: err}
	})
}

type createdEditMsg struct {
	path, original, ns string
	err                error
}

func editorCmd(file string) *exec.Cmd {
	editor := os.Getenv("KUBE_EDITOR")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad"
		}
	}
	parts := strings.Fields(editor)
	return exec.Command(parts[0], append(parts[1:], file)...)
}

func (m *Model) afterCreateEdit(msg createdEditMsg) tea.Cmd {
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.setErr(msg.err)
		return nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.setErr(err)
		return nil
	}
	if string(data) == msg.original || strings.TrimSpace(stripComments(string(data))) == "" {
		m.status, m.isErr = "nothing created", false
		return nil
	}
	return m.validateAndCreate(string(data), msg.ns)
}

func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func resultsText(res []kube.CreateResult) (string, bool) {
	var b strings.Builder
	ok := true
	for _, r := range res {
		where := r.Name
		if r.Namespace != "" {
			where = r.Namespace + "/" + r.Name
		}
		if r.OK {
			fmt.Fprintf(&b, "%s %s %s\n", stOK.Render("✓"), r.Kind, where)
		} else {
			ok = false
			fmt.Fprintf(&b, "%s %s %s: %s\n", stBad.Render("✕"), r.Kind, where, r.Error)
		}
	}
	return b.String(), ok
}

func (m *Model) validateAndCreate(text, ns string) tea.Cmd {
	c := m.cluster
	m.status, m.isErr = "validating (server dry run)…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := c.CreateFromYAML(ctx, text, ns, true)
		if err != nil {
			return doneMsg{err: err}
		}
		body, ok := resultsText(res)
		return validatedMsg{text: text, ns: ns, body: body, ok: ok, n: len(res)}
	}
}

type validatedMsg struct {
	text, ns, body string
	ok             bool
	n              int
}

func (m *Model) afterValidate(msg validatedMsg) tea.Cmd {
	if !msg.ok {
		m.showText("Create: validation failed (nothing was created)", msg.body)
		return nil
	}
	c := m.cluster
	what := fmt.Sprintf("create %d object(s): %s", msg.n, strings.TrimSpace(stripANSI(strings.ReplaceAll(msg.body, "\n", "; "))))
	row := kube.ClusterRow{Context: c.Context, Row: kube.Row{Name: short(c.Context)}}
	return m.guard(truncate(what, 90), row, func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := c.CreateFromYAML(ctx, msg.text, msg.ns, false)
		if err != nil {
			return "", err
		}
		body, ok := resultsText(res)
		if !ok {
			return "", fmt.Errorf("%s", strings.TrimSpace(stripANSI(strings.ReplaceAll(body, "\n", "; "))))
		}
		return fmt.Sprintf("created %d object(s)", len(res)), nil
	})
}

// ---------------------------------------------------------------- files

// cpCmd copies between this computer and the selected pod (or node):
//
//	:cp <remote path> [local dir]     download (default ~/Downloads); a folder comes as .tar
//	:cp put <local path> [remote dir] upload (default /tmp; /host/tmp on a node)
//	:ls [remote dir]                   list a directory
func (m *Model) cpCmd(cmd string, args []string) tea.Cmd {
	row, c, ok := m.selected()
	id := m.kind.ID()
	if !ok || (id != "pods" && id != "nodes") {
		m.status, m.isErr = "select a pod or a node first, then :"+cmd, true
		return nil
	}
	node := id == "nodes"
	target := func(ctx context.Context) (ns, pod, container, root string, done func(), err error) {
		done = func() {}
		if node {
			ns, pod, err = c.StartNodeShell(ctx, row.Name)
			if err != nil {
				return
			}
			done = func() { c.StopNodeShell(ns, pod) }
			return ns, pod, "shell", "/host", done, nil
		}
		cs, err := c.Containers(ctx, row.Namespace, row.Name)
		if err != nil {
			return
		}
		return row.Namespace, row.Name, cs[len(cs)-1], "", done, nil
	}
	where := row.Name
	if node {
		where = "node " + row.Name
	}
	switch cmd {
	case "ls":
		dir := "/"
		if len(args) > 0 {
			dir = args[0]
		}
		m.status, m.isErr = "listing "+dir+" in "+where+"…", false
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ns, pod, ctr, root, done, err := target(ctx)
			if err != nil {
				return textMsg{err: err}
			}
			defer done()
			list, err := c.ListDir(ctx, ns, pod, ctr, root+dir)
			if err != nil {
				return textMsg{err: err}
			}
			rows := [][]string{{"NAME", "SIZE", "MODIFIED"}}
			for _, e := range list {
				name, size := e.Name, humanSize(e.Size)
				if e.Dir {
					name, size = stCmd.Render(name+"/"), "-"
				} else if e.Link != "" {
					name += stFaint.Render(" → " + e.Link)
				}
				mod := "-"
				if !e.ModTime.IsZero() {
					mod = e.ModTime.Format("2006-01-02 15:04")
				}
				rows = append(rows, []string{name, size, mod})
			}
			return textMsg{title: fmt.Sprintf("%s:%s  ·  :cp %s/<name> downloads", where, path.Clean("/"+dir), strings.TrimRight(path.Clean("/"+dir), "/")), body: table(rows)}
		}
	case "put":
		if len(args) == 0 {
			m.status, m.isErr = "usage: :cp put <local file> [remote dir]", true
			return nil
		}
		local := expandHome(args[0])
		dir := "/tmp"
		if len(args) > 1 {
			dir = args[1]
		}
		data, err := os.ReadFile(local)
		if err != nil {
			m.setErr(err)
			return nil
		}
		if len(data) > kube.MaxUpload {
			m.status, m.isErr = fmt.Sprintf("%s is larger than %d MB", local, kube.MaxUpload>>20), true
			return nil
		}
		name := filepath.Base(local)
		return m.guard(fmt.Sprintf("copy %s (%s) into %s:%s", name, humanSize(int64(len(data))), where, dir), row, func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			archive, err := kube.TarOf(map[string][]byte{name: data})
			if err != nil {
				return "", err
			}
			ns, pod, ctr, root, done, err := target(ctx)
			if err != nil {
				return "", err
			}
			defer done()
			if err := c.Upload(ctx, ns, pod, ctr, root+dir, archive); err != nil {
				return "", err
			}
			return fmt.Sprintf("copied %s to %s:%s", name, where, path.Join(dir, name)), nil
		})
	}
	// download
	if len(args) == 0 {
		m.status, m.isErr = "usage: :cp <remote path> [local dir]   (:ls / to browse; :cp put <file> to upload)", true
		return nil
	}
	remote := args[0]
	dst := ""
	if len(args) > 1 {
		dst = expandHome(args[1])
	} else {
		home, _ := os.UserHomeDir()
		dst = filepath.Join(home, "Downloads")
	}
	m.status, m.isErr = "copying "+remote+" from "+where+"…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		ns, pod, ctr, root, done, err := target(ctx)
		if err != nil {
			return doneMsg{err: err}
		}
		defer done()
		var buf bytes.Buffer
		if err := c.Download(ctx, ns, pod, ctr, root+remote, &buf); err != nil {
			return doneMsg{err: err}
		}
		name, data, single := kube.SingleFile(buf.Bytes())
		if !single {
			name, data = path.Base(path.Clean("/"+remote))+".tar", buf.Bytes()
			if name == "/.tar" {
				name = "root.tar"
			}
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return doneMsg{err: err}
		}
		p := uniqueLocal(dst, safeName(name))
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: fmt.Sprintf("saved %s (%s)", p, humanSize(int64(len(data))))}
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func safeName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r == ':' {
			return '_'
		}
		return r
	}, n)
	if n = strings.TrimLeft(n, "."); n == "" {
		n = "download"
	}
	return n
}

func uniqueLocal(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---------------------------------------------------------------- merged logs

// mergedLogs follows several pods at once, interleaved by time, each line prefixed
// with its pod in a colour of its own. Used for marked pods and for "every pod of
// this workload" (l on a Deployment, StatefulSet, DaemonSet, ReplicaSet or Job).
func (m *Model) mergedLogs(pods []kube.ClusterRow, title string) tea.Cmd {
	if len(pods) == 0 {
		m.status, m.isErr = "no pods to show logs for", true
		return nil
	}
	if len(pods) > 20 {
		m.status, m.isErr = fmt.Sprintf("%d pods; showing the first 20", len(pods)), false
		pods = pods[:20]
	}
	m.stopLogs()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan string, 2048)
	m.logCancel, m.logCh, m.logLines, m.logFollow, m.logFind = cancel, ch, nil, true, ""
	m.logPod = kube.ClusterRow{Row: kube.Row{Name: title}}
	m.containers, m.container, m.previous = []string{fmt.Sprintf("%d pods", len(pods))}, 0, false
	m.merged = true
	m.mode = modeLogs
	// Label each line with what tells the pods apart: "5f8866-x2k9p", not the
	// workload name every one of them starts with.
	common := pods[0].Name
	for _, p := range pods[1:] {
		for !strings.HasPrefix(p.Name, common) {
			common = common[:len(common)-1]
		}
	}
	if len(pods) == 1 {
		common = ""
	}
	if i := strings.LastIndex(common, "-"); i >= 0 {
		common = common[:i+1]
	} else {
		common = ""
	}
	label := func(n string) string { return strings.TrimPrefix(n, common) }
	width := 0
	for _, p := range pods {
		width = max(width, len(label(p.Name)))
	}
	width = min(width, 32)
	go func() {
		defer close(ch)
		type line struct {
			ts   string
			text string
		}
		in := make(chan line, 2048)
		done := make(chan struct{}, len(pods))
		for i, p := range pods {
			p, st := p, podColors[i%len(podColors)]
			go func() {
				defer func() { done <- struct{}{} }()
				c, err := m.mgr.Cluster(p.Context)
				if err != nil {
					return
				}
				cs, err := c.Containers(ctx, p.Namespace, p.Name)
				if err != nil || len(cs) == 0 {
					in <- line{text: st.Render(padTo(truncate(label(p.Name), width), width)) + " " + stBad.Render("cannot read: "+firstLine(fmt.Sprint(err)))}
					return
				}
				out := make(chan string, 256)
				go func() {
					_ = c.Logs(ctx, p.Namespace, p.Name, kube.LogOptions{Container: cs[len(cs)-1], Follow: true, TailLines: 200, Timestamps: true}, out)
				}()
				prefix := st.Render(padTo(truncate(label(p.Name), width), width)) + " "
				for l := range out {
					ts, rest, _ := strings.Cut(l, " ")
					select {
					case in <- line{ts: ts, text: prefix + colorLog(rest)}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		left := len(pods)
		// Lines arriving within 300 ms of each other are sorted by time before they
		// are shown, so the initial tail of every pod interleaves correctly.
		var batch []line
		flush := func() {
			sort.SliceStable(batch, func(i, j int) bool { return tsLess(batch[i].ts, batch[j].ts) })
			for _, l := range batch {
				select {
				case ch <- l.text:
				case <-ctx.Done():
					return
				}
			}
			batch = batch[:0]
		}
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for left > 0 {
			select {
			case l := <-in:
				batch = append(batch, l)
			case <-done:
				left--
			case <-t.C:
				flush()
			case <-ctx.Done():
				return
			}
		}
		for {
			select {
			case l := <-in:
				batch = append(batch, l)
			default:
				flush()
				return
			}
		}
	}()
	m.renderLogs()
	return m.waitLog()
}

// tsLess orders RFC3339Nano timestamps; trailing zeros are dropped in that format,
// so they are compared as seconds and nanoseconds, not as strings.
func tsLess(a, b string) bool {
	ta, ea := time.Parse(time.RFC3339Nano, a)
	tb, eb := time.Parse(time.RFC3339Nano, b)
	if ea != nil || eb != nil {
		return a < b
	}
	return ta.Before(tb)
}

var podColors = []lipStyle{stCmd, stOK, stWarn, stPod4, stPod5, stPod6}

// workloadLogs is l on a workload: every pod it owns, merged.
func (m *Model) workloadLogs() tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	k := m.kind
	m.status, m.isErr = "finding the pods of "+row.Name+"…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rows, err := c.PodsOf(ctx, k, row.Namespace, row.Name)
		if err != nil {
			return doneMsg{err: err}
		}
		var pods []kube.ClusterRow
		for _, r := range rows {
			pods = append(pods, kube.ClusterRow{Context: row.Context, Row: r})
		}
		return mergedReadyMsg{pods: pods, title: k.Kind + " " + row.Name}
	}
}

type mergedReadyMsg struct {
	pods  []kube.ClusterRow
	title string
}

func hasPods(k kube.Kind) bool {
	switch k.ID() {
	case "deployments.apps", "statefulsets.apps", "daemonsets.apps", "replicasets.apps", "jobs.batch":
		return true
	}
	return false
}

// ---------------------------------------------------------------- log colours

// colorLog colours one log line the way the window does: the level, timestamps,
// key=value pairs, quoted strings, numbers, URLs, and 4xx/5xx in access logs.
var (
	reLevel = regexp.MustCompile(`(?i)\b(fatal|panic|error|err|warn(?:ing)?|info|debug|trace)\b`)
	reToken = regexp.MustCompile(`https?://[^\s"'<>]+|\b\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(?:[.,]\d+)?(?:Z|[+-]\d\d:?\d\d)?\b|"(?:[^"\\]|\\.)*"|\b[A-Za-z_][\w.\-/]*=|\b\d+(?:\.\d+)?(?:ms|s|µs|ns)?\b`)
	reHTTP  = regexp.MustCompile(`(?i)\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|HTTP/\d|status[=:]|code[=:])`)
	reCode  = regexp.MustCompile(`^[1-5]\d\d$`)
)

func colorLog(s string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	lvl := ""
	if m := reLevel.FindString(s); m != "" {
		lvl = strings.ToLower(m)
	}
	httpish := reHTTP.MatchString(s)
	out := reToken.ReplaceAllStringFunc(s, func(t string) string {
		switch {
		case strings.HasPrefix(t, "http"):
			return stCmd.Underline(true).Render(t)
		case strings.HasPrefix(t, `"`):
			return stOK.Render(t)
		case strings.HasSuffix(t, "="):
			return stFaint.Render(t)
		case len(t) >= 10 && t[4] == '-':
			return stFaint.Render(t)
		case httpish && reCode.MatchString(t):
			if t[0] >= '4' {
				return stBad.Render(t)
			}
			return stOK.Render(t)
		}
		return stArg.Render(t)
	})
	out = reLevel.ReplaceAllStringFunc(out, func(t string) string {
		switch l := strings.ToLower(t); {
		case l == "error" || l == "err" || l == "fatal" || l == "panic":
			return stErr.Render(t)
		case strings.HasPrefix(l, "warn"):
			return stWarn.Bold(true).Render(t)
		case l == "info":
			return stCmd.Render(t)
		}
		return stFaint.Render(t)
	})
	switch {
	case lvl == "error" || lvl == "err" || lvl == "fatal" || lvl == "panic":
		return stBad.Render("▌") + out
	case strings.HasPrefix(lvl, "warn"):
		return stWarn.Render("▌") + out
	}
	return " " + out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func gib(b int64) string {
	g := float64(b) / (1 << 30)
	if g < 10 {
		return fmt.Sprintf("%.1f", g)
	}
	return fmt.Sprintf("%.0f", g)
}

func orAllNS(ns string) string {
	if ns == "" {
		return "all namespaces"
	}
	return ns
}

// helmTopologyText is each release as a tree of what it created, grouped by kind.
func helmTopologyText(hm *kube.HelmMap) string {
	if len(hm.Releases) == 0 {
		return stDim.Render("No Helm releases here.")
	}
	var b strings.Builder
	bad := 0
	for _, r := range hm.Releases {
		if r.Health == kube.Failing || r.Health == kube.Warning {
			bad++
		}
	}
	fmt.Fprintf(&b, " %d releases · %d to look at   %s\n", len(hm.Releases), bad, stDim.Render("● healthy/warning/failing   ✕ in the manifest but missing from the cluster"))
	for _, r := range hm.Releases {
		fmt.Fprintf(&b, "\n %s %s %s\n", healthDot(r.Health), stKey.Render(r.Namespace+"/"+r.Name),
			stDim.Render(fmt.Sprintf("%s-%s · rev %d · %s · %d resources", r.Chart, r.ChartVersion, r.Revision, r.Status, r.Counts.Total)))
		var groups [][]kube.HelmObject
		for _, o := range r.Objects {
			if n := len(groups); n > 0 && groups[n-1][0].Kind == o.Kind {
				groups[n-1] = append(groups[n-1], o)
			} else {
				groups = append(groups, []kube.HelmObject{o})
			}
		}
		for gi, g := range groups {
			last := gi == len(groups)-1
			branch, next := "├─ ", "│  "
			if last {
				branch, next = "└─ ", "   "
			}
			// Healthy groups of config/RBAC collapse to one line; anything not healthy is listed.
			var odd []kube.HelmObject
			for _, o := range g {
				if o.Missing || (o.Health != kube.Healthy && o.Health != "") {
					odd = append(odd, o)
				}
			}
			names := make([]string, 0, len(g))
			for _, o := range g {
				names = append(names, o.Name)
			}
			collapse := len(g) > 3 && kindCollapses(g[0].Kind)
			if collapse {
				fmt.Fprintf(&b, "   %s%s %s\n", stFaint.Render(branch), stDim.Render(fmt.Sprintf("%s ×%d", g[0].Kind, len(g))),
					stFaint.Render(truncate(strings.Join(names, ", "), 90)))
				for _, o := range odd {
					fmt.Fprintf(&b, "   %s   %s\n", stFaint.Render(next), objLine(o))
				}
				continue
			}
			fmt.Fprintf(&b, "   %s%s\n", stFaint.Render(branch), stDim.Render(g[0].Kind))
			for i, o := range g {
				ob := "├─ "
				if i == len(g)-1 {
					ob = "└─ "
				}
				fmt.Fprintf(&b, "   %s%s%s\n", stFaint.Render(next), stFaint.Render(ob), objLine(o))
			}
		}
	}
	if len(hm.Warnings) > 0 {
		b.WriteString("\n" + stWarn.Render(" not checked: "+strings.Join(hm.Warnings, "; ")) + "\n")
	}
	return b.String()
}

func kindCollapses(kind string) bool {
	switch kind {
	case "ConfigMap", "Secret", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding",
		"CustomResourceDefinition", "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		return true
	}
	return false
}

func objLine(o kube.HelmObject) string {
	name := o.Name
	if o.Namespace != "" {
		name = o.Namespace + "/" + o.Name
	}
	if o.Missing {
		return stBad.Render("✕ "+name) + "  " + stBad.Render("missing from the cluster")
	}
	st := stDim
	switch o.Health {
	case kube.Failing:
		st = stBad
	case kube.Warning:
		st = stWarn
	case kube.Healthy:
		st = stOK
	}
	return healthDot(o.Health) + " " + name + "  " + st.Render(o.Status)
}

// ---------------------------------------------------------------- cost

// costCmd is :cost [1d|7d|30d|month] [namespace|workload|pod|node|team] -- from the
// cluster's OpenCost, like the window's Cost page. In a namespace (:ns) it shows
// that namespace's workloads.
func (m *Model) costCmd(args []string) tea.Cmd {
	if m.cluster == nil {
		return nil
	}
	c, ns := m.cluster, m.namespace
	window, agg := "1d", "namespace"
	if ns != "" {
		agg = "controller"
	}
	for _, a := range args {
		switch a {
		case "1d", "24h", "7d", "30d", "month", "today", "week":
			window = map[string]string{"24h": "1d"}[a]
			if window == "" {
				window = a
			}
		case "namespace", "ns":
			agg = "namespace"
		case "workload", "workloads", "controller":
			agg = "controller"
		case "pod", "pods":
			agg = "pod"
		case "node", "nodes":
			agg = "node"
		case "team":
			agg = "label:team"
		}
	}
	m.status, m.isErr = "asking OpenCost…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		rep, err := c.Cost(ctx, window, agg, ns, false)
		if err == kube.ErrNoCost {
			return textMsg{title: "Cost", body: "This cluster runs no OpenCost or Kubecost, so there is no cost data.\n\n" +
				"OpenCost is free and open source:\n  helm repo add opencost https://opencost.github.io/opencost-helm-chart\n" +
				"  helm install opencost opencost/opencost -n opencost --create-namespace\n"}
		}
		if err != nil {
			return textMsg{err: err}
		}
		return textMsg{title: fmt.Sprintf("Cost · %s · %s · %s", short(c.Context), orAllNS(ns), window), body: costText(rep)}
	}
}

func money(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("$%.1fk", v/1000)
	case v >= 100:
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func costText(rep *kube.CostReport) string {
	var b strings.Builder
	days := map[string]float64{"1d": 1, "7d": 7, "30d": 30, "month": float64(time.Now().Day())}[rep.Window]
	if days == 0 {
		days = 7
	}
	fmt.Fprintf(&b, " %s %s   %s %s   %s %s   %s %s   %s %s\n", stHeader.Render("TOTAL"), stKey.Render(money(rep.Total)),
		stHeader.Render("PER MONTH"), money(rep.Monthly), stHeader.Render("PER DAY"), money(rep.Total/days),
		stHeader.Render("IDLE"), money(rep.Idle), stHeader.Render("COULD SAVE"), stWarn.Render(money(rep.Waste)))
	b.WriteString(stDim.Render(fmt.Sprintf(" from %s · idle = node cost nobody requested · could save = requested but unused CPU/memory", rep.Source)) + "\n\n")
	max := 0.0
	for _, r := range rep.Rows {
		if r.Total > max {
			max = r.Total
		}
	}
	first := map[string]string{"namespace": "NAMESPACE", "controller": "WORKLOAD", "pod": "POD", "node": "NODE", "label:team": "TEAM"}[rep.Aggregate]
	rows := [][]string{{first, "", "COST", "PER MONTH", "CPU USED", "MEM USED", "COULD SAVE"}}
	eff := func(x float64) string {
		if x == 0 {
			return stFaint.Render("—")
		}
		s := fmt.Sprintf("%d%%", int(x*100))
		switch {
		case x < 0.2:
			return stBad.Render(s)
		case x < 0.5:
			return stWarn.Render(s)
		}
		return stOK.Render(s)
	}
	for _, r := range rep.Rows {
		n := 0
		if max > 0 {
			n = int(r.Total / max * 20)
		}
		name := r.Name
		if name == "__unallocated__" {
			name = "unallocated"
		}
		waste := stFaint.Render("—")
		if r.Waste > 0.005 {
			waste = money(r.Waste)
			if r.Waste > r.Total*0.5 && r.Waste > 1 {
				waste = stWarn.Render(waste)
			}
		}
		rows = append(rows, []string{name, stCmd.Render(strings.Repeat("▇", n)) + stFaint.Render(strings.Repeat("·", 20-n)),
			money(r.Total), money(r.Total / days * 30), eff(r.CPUEff), eff(r.RAMEff), waste})
	}
	b.WriteString(table(rows))
	if len(rep.Clusters) > 1 && rep.Aggregate == "namespace" {
		var cl []string
		total := 0.0
		for _, c := range rep.Clusters {
			cl = append(cl, c.Name+" "+money(c.Total))
			total += c.Total
		}
		b.WriteString("\n" + stDim.Render(fmt.Sprintf(" This OpenCost sees %d clusters (%s together): %s", len(rep.Clusters), money(total), strings.Join(cl, " · "))) + "\n")
	}
	b.WriteString("\n" + stDim.Render(" :cost 1d|7d|30d|month  namespace|workload|pod|node|team    (:ns <name> first for one namespace)") + "\n")
	return b.String()
}

// ---------------------------------------------------------------- argo cd

// argoCmd is :argo -- the Argo CD dashboard as text.
func (m *Model) argoCmd() tea.Cmd {
	if m.cluster == nil {
		return nil
	}
	c := m.cluster
	m.status, m.isErr = "reading Argo CD…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		d, err := c.ArgoDashboard(ctx)
		if err != nil {
			return textMsg{err: err}
		}
		if !d.Installed {
			return textMsg{title: "Argo CD", body: stDim.Render("Argo CD isn't installed in this cluster.")}
		}
		return textMsg{title: "Argo CD · " + short(c.Context), body: argoText(d)}
	}
}

func argoText(d *kube.ArgoDashboard) string {
	var b strings.Builder
	bad := d.Health["Degraded"] + d.Health["Missing"]
	fmt.Fprintf(&b, " %s %d   %s %s   %s %s   %s %s   %s %d   %s %d\n\n",
		stHeader.Render("APPS"), d.Apps, stHeader.Render("HEALTHY"), stOK.Render(fmt.Sprint(d.Health["Healthy"])),
		stHeader.Render("DEGRADED/MISSING"), map[bool]string{true: stBad.Render(fmt.Sprint(bad)), false: "0"}[bad > 0],
		stHeader.Render("OUT OF SYNC"), map[bool]string{true: stWarn.Render(fmt.Sprint(d.Sync["OutOfSync"])), false: "0"}[d.Sync["OutOfSync"] > 0],
		stHeader.Render("APPSETS"), d.AppSets, stHeader.Render("PROJECTS"), d.Projects)
	pill := func(s string, good bool) string {
		if good {
			return stOK.Render(s)
		}
		if s == "Degraded" || s == "Missing" {
			return stBad.Render(s)
		}
		return stWarn.Render(s)
	}
	b.WriteString(stSection.Render(" NEEDS ATTENTION") + "\n")
	if len(d.Attention) == 0 {
		b.WriteString("  " + stOK.Render("Every application is healthy and in sync.") + "\n")
	} else {
		rows := [][]string{{"APP", "HEALTH", "SYNC", "LAST SYNC", "DESTINATION"}}
		for _, a := range d.Attention {
			rows = append(rows, []string{a.Name, pill(a.Health, a.Health == "Healthy"), pill(a.Sync, a.Sync == "Synced"),
				orDash(a.Operation) + " " + stFaint.Render(truncate(a.Message, 50)), a.Destination})
		}
		b.WriteString(table(rows))
	}
	b.WriteString("\n" + stSection.Render(" RECENT SYNCS") + "\n")
	rows := [][]string{{"APP", "HEALTH", "SYNC", "RESULT", "WHEN", "REVISION"}}
	for _, a := range d.Recent {
		rows = append(rows, []string{a.Name, pill(a.Health, a.Health == "Healthy"), pill(a.Sync, a.Sync == "Synced"), orDash(a.Operation), ago(a.Finished), a.Revision})
	}
	b.WriteString(table(rows))
	counts := func(title string, cs []kube.ArgoCount) {
		b.WriteString("\n" + stSection.Render(" "+title) + "\n")
		rows := [][]string{{"", "APPS", "HEALTHY", "NEED A LOOK"}}
		for _, c := range cs[:min(8, len(cs))] {
			need := "0"
			if c.Bad > 0 {
				need = stWarn.Render(fmt.Sprint(c.Bad))
			}
			rows = append(rows, []string{c.Name, fmt.Sprint(c.Apps), fmt.Sprint(c.Healthy), need})
		}
		b.WriteString(table(rows))
	}
	counts("BY PROJECT", d.ByProject)
	counts("BY DESTINATION", d.ByDest)
	b.WriteString("\n" + stSection.Render(" APPLICATION SETS") + "\n")
	srows := [][]string{{"NAME", "STATUS", "APPS"}}
	for _, s := range d.AppSetStatus {
		srows = append(srows, []string{s.Name, statusCell(s.Health, s.Status), fmt.Sprint(s.Apps)})
	}
	b.WriteString(table(srows))
	b.WriteString("\n" + stDim.Render(" :app  :applicationsets  :appprojects  list them; :app !ok shows only the ones that need a look") + "\n")
	return b.String()
}

// ---------------------------------------------------------------- warnings

// warningsCmd is :warnings [window] -- the window's Warnings dashboard as text.
func (m *Model) warningsCmd(args []string) tea.Cmd {
	if m.cluster == nil {
		return nil
	}
	c, ns := m.cluster, m.namespace
	window := time.Hour
	if len(args) > 0 {
		if d, err := time.ParseDuration(args[0]); err == nil {
			window = d
		}
	}
	m.status, m.isErr = "reading events…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		d, err := c.Events(ctx, ns, window)
		if err != nil {
			return textMsg{err: err}
		}
		where := "all namespaces"
		if ns != "" {
			where = ns
		}
		return textMsg{title: "Warnings · " + short(c.Context) + " · " + where + " · last " + strings.TrimSuffix(strings.TrimSuffix(window.String(), "0s"), "0m"), body: warningsText(d)}
	}
}

func warningsText(d *kube.EventsDashboard) string {
	var b strings.Builder
	red := 0
	for _, g := range d.Groups {
		if g.Level == kube.Failing {
			red++
		}
	}
	top := func(l []kube.EventCount) string {
		if len(l) == 0 {
			return "-"
		}
		return fmt.Sprintf("%s (%d)", l[0].Name, l[0].Count)
	}
	fmt.Fprintf(&b, " %s %d   %s %d (%s broken)   %s %d   %s %s   %s %s\n",
		stHeader.Render("WARNINGS"), d.Warnings, stHeader.Render("PROBLEMS"), len(d.Groups), stBad.Render(fmt.Sprint(red)),
		stHeader.Render("OBJECTS"), d.Objects, stHeader.Render("NAMESPACE"), top(d.ByNs), stHeader.Render("POOL"), top(d.ByPool))
	for _, e := range d.Errors {
		b.WriteString(" " + stBad.Render(e) + "\n")
	}
	b.WriteString("\n" + stSection.Render(" PROBLEMS") + stDim.Render("  broken first") + "\n")
	if len(d.Groups) == 0 {
		b.WriteString("  " + stOK.Render("No warnings in this window.") + "\n")
		return b.String()
	}
	for i, g := range d.Groups {
		if i == 30 {
			fmt.Fprintf(&b, "  %s\n", stDim.Render(fmt.Sprintf("… %d more", len(d.Groups)-30)))
			break
		}
		reason := stWarn.Render(g.Reason)
		if g.Level == kube.Failing {
			reason = stBad.Render(g.Reason)
		}
		where := strings.Join(g.Namespaces, ",")
		if len(g.Namespaces) > 2 {
			where = fmt.Sprintf("%d namespaces", len(g.Namespaces))
		}
		if len(g.Pools) > 0 {
			where += " · pool " + strings.Join(g.Pools, ",")
		}
		fmt.Fprintf(&b, "\n  %s ×%d  %s\n", reason, g.Count, stDim.Render(fmt.Sprintf("%d %s · %s · last %s ago", g.Objects, strings.ToLower(g.Kind), where, shortAgo(g.Last))))
		msg := g.Message
		if len(msg) > 220 {
			msg = msg[:220] + "…"
		}
		b.WriteString("    " + msg + "\n")
		if g.Hint != "" {
			b.WriteString("    " + stDim.Render("→ "+g.Hint) + "\n")
		}
		names := make([]string, 0, len(g.Examples))
		for _, o := range g.Examples {
			if o.Namespace != "" {
				names = append(names, o.Namespace+"/"+o.Name)
			} else {
				names = append(names, o.Name)
			}
		}
		more := ""
		if g.Objects > len(g.Examples) {
			more = fmt.Sprintf(" +%d more", g.Objects-len(g.Examples))
		}
		b.WriteString("    " + stDim.Render(strings.Join(names, "  ")+more) + "\n")
	}
	return b.String()
}

func shortAgo(t time.Time) string {
	s := time.Since(t)
	switch {
	case s < time.Minute:
		return fmt.Sprintf("%ds", int(s.Seconds()))
	case s < time.Hour:
		return fmt.Sprintf("%dm", int(s.Minutes()))
	case s < 48*time.Hour:
		return fmt.Sprintf("%dh", int(s.Hours()))
	}
	return fmt.Sprintf("%dd", int(s.Hours()/24))
}
