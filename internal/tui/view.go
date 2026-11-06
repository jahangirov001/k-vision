package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/njahangirov/kube-console/internal/kube"
)

// Colours adapt to the terminal's own background (light or dark), the terminal
// equivalent of the desktop app's System theme. KV_THEME=light|dark forces one.
var (
	cAccent = adaptive("#2459D6", "#7C9DFF")
	cText   = adaptive("#17202C", "#E4E9F1")
	cMuted  = adaptive("#5E6B7C", "#93A0B2")
	cFaint  = adaptive("#8B96A6", "#5D6A7D")
	cLine   = adaptive("#D9DFE8", "#2A3240")
	cSel    = adaptive("#E3EBFD", "#1F2D4A")
	cBar    = adaptive("#E9EDF3", "#151B24")
	cOK     = adaptive("#1D7A43", "#4CC77D")
	cWarn   = adaptive("#9A5B00", "#F2B84B")
	cBad    = adaptive("#C0262D", "#FF6B6B")
	cProd   = adaptive("#B42318", "#FF8A80")
	cProdBg = adaptive("#FFF1F0", "#2A1214")
)

func adaptive(light, dark string) lipgloss.TerminalColor {
	switch os.Getenv("KV_THEME") {
	case "light":
		return lipgloss.Color(light)
	case "dark":
		return lipgloss.Color(dark)
	}
	return lipgloss.AdaptiveColor{Light: light, Dark: dark}
}

var (
	stBrand   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stKind    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	stDim     = lipgloss.NewStyle().Foreground(cMuted)
	stFaint   = lipgloss.NewStyle().Foreground(cFaint)
	stHeader  = lipgloss.NewStyle().Bold(true).Foreground(cFaint)
	stCursor  = lipgloss.NewStyle().Background(cSel).Foreground(cText).Bold(true)
	stOK      = lipgloss.NewStyle().Foreground(cOK)
	stWarn    = lipgloss.NewStyle().Foreground(cWarn)
	stBad     = lipgloss.NewStyle().Foreground(cBad)
	stBadText = lipgloss.NewStyle().Foreground(cBad).Bold(true)
	stErr     = lipgloss.NewStyle().Foreground(cBad).Bold(true)
	stSection = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stCmd     = lipgloss.NewStyle().Foreground(cAccent)
	stArg     = lipgloss.NewStyle().Foreground(cWarn)
	stExample = lipgloss.NewStyle().Foreground(cFaint).Italic(true)
	stKey     = lipgloss.NewStyle().Bold(true).Foreground(cText)
	stBar     = lipgloss.NewStyle().Background(cBar)
	stProd    = lipgloss.NewStyle().Bold(true).Foreground(cProd).Background(cProdBg).Padding(0, 1)
	stSafeTag = lipgloss.NewStyle().Bold(true).Foreground(cOK).Padding(0, 1)
	stAllTag  = lipgloss.NewStyle().Bold(true).Foreground(cAccent).Padding(0, 1)
	stTabOn   = lipgloss.NewStyle().Bold(true).Foreground(cText).Underline(true)
	stTabOff  = lipgloss.NewStyle().Foreground(cMuted)
	stPrompt  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stSugOn   = lipgloss.NewStyle().Background(cSel).Foreground(cText)
	stBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cLine).Padding(0, 1)
	stPod4    = lipgloss.NewStyle().Foreground(adaptive("#8E3BB8", "#C792EA"))
	stPod5    = lipgloss.NewStyle().Foreground(adaptive("#0F7C8C", "#5FD4E0"))
	stPod6    = lipgloss.NewStyle().Foreground(adaptive("#B5487A", "#F78FB3"))
)

type lipStyle = lipgloss.Style

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	head := m.header()
	foot := m.footer()
	bodyH := m.height - lipgloss.Height(head) - lipgloss.Height(foot)
	var body string
	switch m.mode {
	case modeText, modeLogs:
		m.view.Height = max(1, bodyH)
		m.view.Width = m.width
		body = m.view.View()
	case modeContexts:
		body = m.contextsView(bodyH)
	default:
		body = m.tableView(bodyH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, fit(body, m.width, bodyH), foot)
}

// fit pads or cuts a block to exactly w×h so the footer never jumps.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:max(0, h)]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(strings.Join(lines, "\n"))
}

func (m *Model) header() string {
	var ctx string
	switch {
	case m.everywhere:
		ctx = stAllTag.Render("◎ every cluster")
	case m.mgr.Protected(m.ctxName):
		ctx = stProd.Render("● " + short(m.ctxName))
	default:
		ctx = stSafeTag.Render("○ " + short(m.ctxName))
	}
	ns := m.namespace
	if ns == "" {
		ns = "all namespaces"
	}
	what := m.kind.Kind
	if what == "" {
		what = m.kind.ID()
	}
	switch m.mode {
	case modeText:
		what = m.viewTitle
	case modeLogs:
		what = "Logs  " + m.logPod.Name + stDim.Render(" ["+m.containers[m.container]+"]")
		if m.previous {
			what += stWarn.Render("  previous")
		}
		if !m.logFollow {
			what += stDim.Render("  paused")
		}
		if m.logFind != "" {
			what += stCmd.Render("  find: " + m.logFind)
		}
	case modeContexts:
		what = "Clusters"
	}
	left := stBrand.Render(" ⎈ K-Vision ") + ctx + "  " + stKind.Render(what) + stDim.Render("  "+ns)
	right := ""
	if m.loading {
		right = stDim.Render("refreshing… ")
	} else if !m.lastLoad.IsZero() && m.mode == modeTable {
		right = stDim.Render("live ")
	}
	line1 := spread(left, right, m.width)
	if m.mode != modeTable {
		return stBar.Width(m.width).Render(line1)
	}
	return stBar.Width(m.width).Render(line1) + "\n" + m.tabs()
}

// tabs is the health bar under the header: counts per state, the active one
// underlined, then the filter if one is set.
func (m *Model) tabs() string {
	counts := map[kube.Health]int{}
	total := 0
	for _, i := range m.filtered {
		counts[m.rows[i].Health]++
		total++
	}
	tab := func(id kube.Health, label string, n int, dot string) string {
		s := label + " " + fmt.Sprint(n)
		if dot != "" {
			s = dot + " " + s
		}
		if m.health == id {
			return stTabOn.Render(s)
		}
		return stTabOff.Render(s)
	}
	parts := []string{
		tab("all", "All", total, ""),
		tab(kube.Failing, "Failing", counts[kube.Failing], stBad.Render("●")),
		tab(kube.Warning, "Warning", counts[kube.Warning], stWarn.Render("●")),
		tab(kube.Healthy, "Healthy", counts[kube.Healthy], stOK.Render("●")),
	}
	s := " " + strings.Join(parts, stFaint.Render("   "))
	if m.everywhere {
		per := map[string]int{}
		for _, i := range m.filtered {
			per[m.rows[i].Context]++
		}
		var cl []string
		for _, c := range m.mgr.Contexts() {
			if n := per[c.Name]; n > 0 {
				cl = append(cl, clusterTag(c.Name, c.Protected)+stFaint.Render(fmt.Sprintf(" %d", n)))
			}
		}
		if len(cl) > 0 {
			s += "\n " + strings.Join(cl, stFaint.Render("  ·  ")) + stFaint.Render("    (ctx:<name> narrows to one)")
		}
	}
	if m.filter != "" {
		s += stFaint.Render("     filter ") + stCmd.Render(m.filter) + stFaint.Render("  (esc clears)")
	}
	return s
}

func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) footer() string {
	if m.purpose != inNone {
		return m.promptView()
	}
	if m.status != "" {
		st := stOK
		icon := "✓ "
		if m.isErr {
			st, icon = stErr, "✕ "
		}
		return " " + truncate(st.Render(icon+m.status), m.width-1)
	}
	var keys [][2]string
	switch m.mode {
	case modeLogs:
		keys = [][2]string{{"esc", "back"}, {"/", "find"}, {"F", "follow"}, {"P", "previous"}, {"c", "container"}, {"w", "save"}}
		if m.merged {
			keys = [][2]string{{"esc", "back"}, {"/", "find"}, {"F", "follow"}, {"w", "save"}}
		}
	case modeText:
		keys = [][2]string{{"esc", "back"}, {"↑↓", "scroll"}, {"pgup/pgdn", "page"}}
	case modeContexts:
		keys = [][2]string{{"enter", "switch"}, {"h", "remove from list / put back"}, {"d", "delete from kubeconfig"}, {"↑↓", "move"}, {"esc", "back"}}
	default:
		keys = [][2]string{{":", "command"}, {"/", "filter"}, {"enter", "yaml"}, {"x", "why"}, {"l", "logs"},
			{"s", "shell"}, {"e", "edit"}, {"N", "create"}, {"^d", "delete"}, {"C", "cluster"}, {"?", "help"}}
		if id := m.kind.ID(); id == "secrets" || id == "configmaps" {
			keys = append([][2]string{{"v", "values"}}, keys...)
		}
	}
	if n := len(m.marked); n > 0 && m.mode == modeTable {
		keys = append([][2]string{{fmt.Sprintf("%d marked", n), ""}, {"^d", "delete marked"}, {"space", "mark"}, {"esc", "clear"}}, keys[:4]...)
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, stKey.Render(k[0])+" "+stDim.Render(k[1]))
	}
	return " " + truncate(strings.Join(parts, stFaint.Render("  ·  ")), m.width-1)
}

// promptView is the input line, with suggestions above it while typing a command.
func (m *Model) promptView() string {
	line := " " + m.input.View()
	if m.purpose != inCommand {
		if m.purpose == inFilter {
			line += "\n " + stFaint.Render("text  -exclude  !ok  ns:x  status:a|b  restarts>3  age<1h  app=web     enter keeps · esc clears")
		}
		return line
	}
	sugs := m.suggestions
	if len(sugs) == 0 {
		return line + "\n " + stFaint.Render("tab completes · ↑↓ choose · enter runs · esc cancels · :help lists everything")
	}
	var rows []string
	for i, s := range sugs {
		text := s.text
		if s.args != "" {
			text += " " + stArg.Render(s.args)
		}
		pad := 30 - lipgloss.Width(text)
		if pad < 2 {
			pad = 2
		}
		row := "  " + text + strings.Repeat(" ", pad) + stDim.Render(s.hint)
		if i == m.sugIndex {
			row = stSugOn.Render(truncate("▸ "+s.text+argsOf(s)+strings.Repeat(" ", pad)+s.hint, m.width-2))
		}
		rows = append(rows, truncate(row, m.width-2))
	}
	box := stBox.Width(min(m.width-2, 90)).Render(strings.Join(rows, "\n"))
	return box + "\n" + line
}

func argsOf(s suggestion) string {
	if s.args == "" {
		return ""
	}
	return " " + s.args
}

func (m *Model) tableView(h int) string {
	if h < 2 {
		return ""
	}
	cols := []string{}
	if m.everywhere {
		cols = append(cols, "CLUSTER")
	}
	if m.kind.Namespaced {
		cols = append(cols, "NAMESPACE")
	}
	cols = append(cols, "NAME", "STATUS")
	cols = append(cols, m.cols...)
	cols = append(cols, "AGE")

	rowsH := h - 1
	m.pageH = rowsH
	end := min(len(m.visible), m.offset+rowsH)
	cells := make([][]string, 0, max(0, end-m.offset))
	for _, idx := range m.visible[m.offset:end] {
		r := m.rows[idx]
		var line []string
		if m.everywhere {
			line = append(line, clusterTag(r.Context, m.mgr.Protected(r.Context)))
		}
		if m.kind.Namespaced {
			line = append(line, r.Namespace)
		}
		line = append(line, r.Name, statusCell(r.Health, r.Status))
		for _, c := range m.cols {
			v := r.Cells[c]
			if c == "CPU" || c == "MEMORY" {
				v = usageText(v)
			}
			line = append(line, v)
		}
		line = append(line, r.Age)
		cells = append(cells, line)
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c)
	}
	for _, line := range cells {
		for i, v := range line {
			widths[i] = max(widths[i], lipgloss.Width(v))
		}
	}
	total := 1
	for _, w := range widths {
		total += w + 2
	}
	for total > m.width && total > 0 {
		wi := 0
		for i := range widths {
			if widths[i] > widths[wi] {
				wi = i
			}
		}
		if widths[wi] <= 8 {
			break
		}
		widths[wi]--
		total--
	}
	var b strings.Builder
	b.WriteString(" " + stHeader.Render(pad(cols, widths)) + "\n")
	for i, line := range cells {
		r := m.rows[m.visible[m.offset+i]]
		_, isMarked := m.marked[markKey(r)]
		if isMarked {
			line[0] = stCmd.Render("✓ ") + line[0]
		}
		if m.offset+i == m.cursor {
			plain := append([]string{}, line...)
			for j := range plain {
				plain[j] = stripANSI(plain[j])
			}
			b.WriteString(stCursor.Render("▌" + padTo(pad(plain, widths), m.width-1)))
		} else {
			b.WriteString(" " + pad(line, widths))
		}
		b.WriteByte('\n')
	}
	if len(m.visible) == 0 {
		msg := "Nothing here."
		switch {
		case m.loading:
			msg = "Loading…"
		case len(m.rows) > 0:
			msg = "No rows match. Esc clears the filter; 1 shows every health state."
		}
		b.WriteString("\n   " + stDim.Render(msg))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// clusterHues gives each cluster a stable colour so a long all-clusters list reads
// as blocks; the marker is red ● for a protected cluster, green ○ for a safe one.
var clusterHues = []string{"#4C8DFF", "#2FB37A", "#E39A2D", "#A66CFF", "#E35D8C", "#2CB5C2", "#8DBB2E", "#E2664B"}

func clusterTag(ctx string, protected bool) string {
	name := short(ctx)
	h := uint32(0)
	for _, r := range name {
		h = h*31 + uint32(r)
	}
	st := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(clusterHues[h%uint32(len(clusterHues))]))
	mark := stOK.Render("○ ")
	if protected {
		mark = stBad.Render("● ")
	}
	return mark + st.Render(name)
}

func statusCell(h kube.Health, s string) string {
	switch h {
	case kube.Failing:
		return stBad.Render("● " + s)
	case kube.Warning:
		return stWarn.Render("● " + s)
	case kube.Healthy:
		return stOK.Render("● ") + s
	}
	return stFaint.Render("○ ") + s
}

func (m *Model) contextsView(h int) string {
	var b strings.Builder
	b.WriteString("\n")
	shownHidden := false
	for i, c := range m.pickerContexts() {
		if c.Hidden && !shownHidden {
			shownHidden = true
			b.WriteString("\n  " + stFaint.Render("REMOVED FROM THE LIST  (h puts back; your kubeconfig still has these)") + "\n")
		}
		tag := stOK.Render("○          ")
		if c.Protected {
			tag = stBad.Render("●          ")
		}
		cur := "  "
		if c.Name == m.ctxName {
			cur = stCmd.Render("✓ ")
		}
		name := fmt.Sprintf("%-32s", short(c.Name))
		if c.Hidden {
			name = stFaint.Render(name)
		}
		line := fmt.Sprintf("  %s%s  %s %s", cur, tag, name, stFaint.Render(c.Name))
		if i == m.ctxCursor {
			line = stCursor.Render(padTo(truncate(fmt.Sprintf("▌ %s%s  %-32s %s", stripANSI(cur), stripANSI(tag), short(c.Name), c.Name), m.width), m.width))
		}
		b.WriteString(truncate(line, m.width) + "\n")
	}
	return b.String()
}

func pad(cells []string, widths []int) string {
	var b strings.Builder
	for i, c := range cells {
		w := widths[i]
		if lipgloss.Width(c) > w {
			c = truncate(c, w)
		}
		b.WriteString(c)
		if i < len(cells)-1 {
			b.WriteString(strings.Repeat(" ", max(0, w-lipgloss.Width(c))+2))
		}
	}
	return b.String()
}

func padTo(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w-1).Render(s) + "…"
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shellCmd runs an interactive exec inside the terminal, with bubbletea suspended.
type shellCmd struct {
	c                  *kube.Cluster
	ns, pod, container string
	command            []string // default: bash, else sh
	stdin              io.Reader
	stdout, stderr     io.Writer
}

func (s *shellCmd) SetStdin(r io.Reader)  { s.stdin = r }
func (s *shellCmd) SetStdout(w io.Writer) { s.stdout = w }
func (s *shellCmd) SetStderr(w io.Writer) { s.stderr = w }

func (s *shellCmd) Run() error {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		old, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, old)
		}
	}
	sizes := &termSizes{fd: int(os.Stdout.Fd())}
	// bash if the image has it, sh otherwise -- the same fallback people type by hand.
	cmd := []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
	if s.command != nil {
		cmd = s.command
	}
	err := s.c.Exec(context.Background(), s.ns, s.pod, s.container, cmd, s.stdin, s.stdout, sizes)
	fmt.Fprint(s.stdout, "\r\n")
	return err
}

type termSizes struct {
	fd   int
	sent bool
}

func (t *termSizes) Next() *remotecommand.TerminalSize {
	if t.sent {
		select {} // one size is enough here; the session ends with the shell
	}
	t.sent = true
	w, h, err := term.GetSize(t.fd)
	if err != nil {
		return &remotecommand.TerminalSize{Width: 120, Height: 40}
	}
	return &remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}
}

// usageText colours a pod's "589Mi 57%" cell: amber from 70%, red from 90% of the
// limit (or request), so a pod about to be throttled or OOM-killed stands out.
func usageText(v string) string {
	val, p, ok := strings.Cut(v, " ")
	if !ok {
		return stFaint.Render(v)
	}
	n := 0
	fmt.Sscanf(p, "%d%%", &n)
	switch {
	case n >= 90:
		return val + " " + stBadText.Render(p)
	case n >= 70:
		return val + " " + stWarn.Render(p)
	}
	return val + " " + stFaint.Render(p)
}
