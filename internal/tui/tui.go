// Package tui is the terminal front end: k9s-style, keyboard only, over the same
// internal/kube layer the desktop app uses.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/njahangirov/kube-console/internal/kube"
)

type mode int

const (
	modeTable mode = iota
	modeText       // YAML, explain, help, port-forwards
	modeLogs
	modeContexts
)

// input purposes: what the text box at the bottom is collecting.
const (
	inNone    = ""
	inCommand = "command"
	inFilter  = "filter"
	inScale   = "scale"
	inForward = "forward"
	inConfirm = "confirm"
	inLogFind = "logfind"
)

// Model is the whole terminal app.
type Model struct {
	mgr *kube.Manager

	ctxName    string
	cluster    *kube.Cluster
	kind       kube.Kind
	namespace  string // "" = all
	everywhere bool   // the current list spans every context

	rows     []kube.ClusterRow
	cols     []string
	filtered []int // indexes into rows that pass the filter
	visible  []int // ...and the health tab
	health   kube.Health
	pageH    int
	cursor   int
	offset   int
	filter   string
	loading  bool
	seq      int // bumps on every new list request; stale results are dropped
	lastLoad time.Time

	mode   mode
	width  int
	height int
	status string
	isErr  bool

	input   textinput.Model
	purpose string
	confirm *pending

	view      viewport.Model
	viewTitle string

	logCancel  context.CancelFunc
	logCh      chan string
	logLines   []string
	logFind    string
	logFollow  bool
	logPod     kube.ClusterRow
	containers []string
	container  int
	previous   bool
	merged     bool // the log view follows several pods at once

	ctxCursor int

	marked map[string]kube.ClusterRow // rows ticked with space, for a bulk delete

	suggestions []suggestion
	sugIndex    int
	nsCache     []string
}

// pending is a write waiting for confirmation.
type pending struct {
	what string // shown in the prompt
	want string // what has to be typed
	run  func() (string, error)
}

// Run starts the terminal UI on the current kubeconfig context.
func Run(mgr *kube.Manager, startKind, startContext string) error {
	m := New(mgr, startKind, startContext)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func New(mgr *kube.Manager, startKind, startContext string) *Model {
	in := textinput.New()
	in.CharLimit = 512
	if startKind == "" {
		startKind = "pods"
	}
	if startContext == "" {
		startContext = mgr.CurrentContext()
	}
	in.PromptStyle = stPrompt
	in.TextStyle = stKey
	in.Cursor.Style = stCmd
	m := &Model{mgr: mgr, ctxName: startContext, input: in, view: viewport.New(80, 20), logFollow: true, health: "all"}
	m.kind = kube.Kind{Name: startKind} // resolved once the cluster is up
	return m
}

// ---------------------------------------------------------------- messages

type clusterMsg struct {
	name    string
	cluster *kube.Cluster
	kind    kube.Kind
	err     error
}
type listMsg struct {
	seq  int
	rows []kube.ClusterRow
	cols []string
	errs []string
	err  error
}
type tickMsg time.Time
type textMsg struct {
	title, body string
	err         error
}
type doneMsg struct {
	text string
	err  error
}
type logMsg struct{ lines []string }
type logEndMsg struct{ err error }
type editedMsg struct {
	path, original string
	row            kube.ClusterRow
	err            error
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.connect(m.ctxName, m.kind.Name), tick())
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) connect(name, kind string) tea.Cmd {
	m.loading = true
	m.status, m.isErr = "connecting to "+short(name)+"…", false
	mgr := m.mgr
	return func() tea.Msg {
		c, err := mgr.Cluster(name)
		if err != nil {
			return clusterMsg{name: name, err: err}
		}
		k, err := c.ResolveKind(kind)
		return clusterMsg{name: name, cluster: c, kind: k, err: err}
	}
}

func (m *Model) reload() tea.Cmd {
	m.seq++
	seq, kind, ns, filter := m.seq, m.kind, m.namespace, ""
	m.loading = true
	if m.everywhere {
		mgr := m.mgr
		var names []string
		for _, c := range mgr.Contexts() {
			names = append(names, c.Name)
		}
		return func() tea.Msg {
			res := mgr.ListEverywhere(context.Background(), names, kind.ID(), ns, filter)
			out := listMsg{seq: seq}
			for _, r := range res {
				if r.Error != "" {
					out.errs = append(out.errs, short(r.Context)+": "+firstLine(r.Error))
					continue
				}
				if out.cols == nil {
					out.cols = r.Columns
				}
				out.rows = append(out.rows, r.Rows...)
			}
			return out
		}
	}
	c := m.cluster
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rows, cols, err := c.List(ctx, kind, ns)
		out := listMsg{seq: seq, cols: cols, err: err}
		for _, r := range rows {
			out.rows = append(out.rows, kube.ClusterRow{Context: c.Context, Row: r})
		}
		return out
	}
}

// ---------------------------------------------------------------- update

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.view.Width, m.view.Height = msg.Width, max(1, msg.Height-3)
		return m, nil

	case clusterMsg:
		m.loading = false
		if msg.err != nil {
			m.setErr(msg.err)
			return m, nil
		}
		m.ctxName, m.cluster, m.kind, m.everywhere = msg.name, msg.cluster, msg.kind, false
		if m.namespace == "" && msg.cluster.Namespace != "" && false {
			m.namespace = msg.cluster.Namespace
		}
		m.status = ""
		m.cursor, m.offset = 0, 0
		return m, m.reload()

	case listMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		m.lastLoad = time.Now()
		if msg.err != nil {
			m.setErr(msg.err)
			return m, nil
		}
		m.rows, m.cols = msg.rows, msg.cols
		if m.everywhere {
			// Same order as the cluster picker, so each cluster's rows stay together.
			order := map[string]int{}
			for i, c := range m.mgr.Contexts() {
				order[c.Name] = i
			}
			sort.SliceStable(m.rows, func(i, j int) bool { return order[m.rows[i].Context] < order[m.rows[j].Context] })
		}
		m.applyFilter()
		if len(msg.errs) > 0 {
			m.status, m.isErr = "unreachable: "+strings.Join(msg.errs, "; "), true
		}
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{tick()}
		// Refresh only an idle table: re-listing under someone reading YAML or typing
		// would move the row they are about to act on.
		every := 5 * time.Second
		if m.everywhere {
			every = 20 * time.Second
		}
		if m.mode == modeTable && m.purpose == inNone && !m.loading && time.Since(m.lastLoad) >= every {
			cmds = append(cmds, m.reload())
		}
		return m, tea.Batch(cmds...)

	case textMsg:
		if msg.err != nil {
			m.setErr(msg.err)
			return m, nil
		}
		m.showText(msg.title, msg.body)
		return m, nil

	case doneMsg:
		if strings.HasPrefix(msg.text, "\x00") {
			parts := strings.SplitN(msg.text[1:], "\x00", 2)
			m.showText(parts[0], parts[1])
			return m, nil
		}
		if msg.err != nil {
			m.setErr(msg.err)
		} else {
			m.status, m.isErr = msg.text, false
		}
		return m, m.reload()

	case logMsg:
		m.logLines = append(m.logLines, msg.lines...)
		if n := len(m.logLines); n > 20000 {
			m.logLines = m.logLines[n-20000:]
		}
		m.renderLogs()
		return m, m.waitLog()

	case logEndMsg:
		if msg.err != nil {
			m.setErr(msg.err)
		} else if m.mode == modeLogs {
			m.status, m.isErr = "log stream ended", false
		}
		return m, nil

	case editedMsg:
		return m, m.afterEdit(msg)

	case createdEditMsg:
		return m, m.afterCreateEdit(msg)

	case validatedMsg:
		m.status = ""
		return m, m.afterValidate(msg)

	case mergedReadyMsg:
		m.status = ""
		return m, m.mergedLogs(msg.pods, msg.title)

	case nodeShellReady:
		m.status = ""
		cmd := &shellCmd{c: msg.c, ns: msg.ns, pod: msg.pod, container: "shell", command: kube.NodeShellCommand}
		return m, tea.Exec(cmd, func(err error) tea.Msg {
			msg.c.StopNodeShell(msg.ns, msg.pod)
			if err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{text: "node shell on " + msg.node + " closed; helper pod removed"}
		})

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) setErr(err error) {
	m.status, m.isErr = firstLine(err.Error()), true
}

// ---------------------------------------------------------------- keys

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		m.stopLogs()
		return m, tea.Quit
	}
	if m.purpose != inNone {
		return m.inputKey(k)
	}
	switch m.mode {
	case modeText:
		return m.textKey(k)
	case modeLogs:
		return m.logKey(k)
	case modeContexts:
		return m.contextKey(k)
	}
	return m.tableKey(k)
}

func (m *Model) refreshSuggestions() {
	m.suggestions = m.complete(m.input.Value())
	if m.sugIndex >= len(m.suggestions) {
		m.sugIndex = 0
	}
}

// isExact: the typed word already names a command or kind, so Enter must not
// replace it with the first suggestion ("po" stays pods, not podtemplates).
func (m *Model) isExact(v string) bool {
	v = strings.ToLower(v)
	for _, c := range Commands {
		if c.Name == v {
			return true
		}
		for _, a := range c.Aliases {
			if a == v {
				return true
			}
		}
	}
	if m.cluster != nil {
		if _, err := m.cluster.ResolveKind(v); err == nil {
			return true
		}
	}
	return false
}

func (m *Model) ask(purpose, prompt, value string) tea.Cmd {
	m.purpose = purpose
	m.suggestions, m.sugIndex = nil, 0
	if purpose == inCommand {
		defer m.refreshSuggestions()
	}
	m.input.Prompt = prompt
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.purpose == inCommand {
		switch k.String() {
		case "tab":
			if len(m.suggestions) > 0 {
				s := m.suggestions[m.sugIndex]
				m.input.SetValue(s.text + " ")
				m.input.CursorEnd()
				m.refreshSuggestions()
			}
			return m, nil
		case "down", "ctrl+n":
			if len(m.suggestions) > 0 {
				m.sugIndex = (m.sugIndex + 1) % len(m.suggestions)
			}
			return m, nil
		case "up", "ctrl+p":
			if len(m.suggestions) > 0 {
				m.sugIndex = (m.sugIndex - 1 + len(m.suggestions)) % len(m.suggestions)
			}
			return m, nil
		case "enter":
			// Enter on a highlighted suggestion that the typed text is only a prefix of
			// runs the suggestion: ":dep" + Enter opens deployments.
			v := strings.TrimSpace(m.input.Value())
			if len(m.suggestions) > 0 && !strings.Contains(v, " ") {
				s := m.suggestions[m.sugIndex]
				if strings.HasPrefix(s.text, strings.ToLower(v)) && !m.isExact(v) {
					m.input.SetValue(s.text)
				}
			}
		}
	}
	switch k.String() {
	case "esc":
		if m.purpose == inFilter {
			m.filter = ""
			m.applyFilter()
		}
		if m.purpose == inLogFind {
			m.logFind = ""
			m.renderLogs()
		}
		m.purpose, m.confirm = inNone, nil
		m.input.Blur()
		m.status = ""
		return m, nil
	case "enter":
		purpose, value := m.purpose, strings.TrimSpace(m.input.Value())
		m.purpose = inNone
		m.input.Blur()
		return m, m.submit(purpose, value)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.purpose == inCommand {
		m.refreshSuggestions()
	}
	// Filters apply as you type, like k9s.
	if m.purpose == inFilter {
		m.filter = m.input.Value()
		m.applyFilter()
	}
	if m.purpose == inLogFind {
		m.logFind = m.input.Value()
		m.renderLogs()
	}
	return m, cmd
}

func (m *Model) submit(purpose, value string) tea.Cmd {
	switch purpose {
	case inCommand:
		return m.command(value)
	case inFilter:
		m.filter = value
		m.applyFilter()
	case inLogFind:
		m.logFind = value
		m.renderLogs()
	case inScale:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			m.status, m.isErr = "replicas must be a number ≥ 0", true
			return nil
		}
		row, c, ok := m.selected()
		if !ok {
			return nil
		}
		k := m.kind
		return m.guard(fmt.Sprintf("scale %s/%s to %d", row.Namespace, row.Name, n), row, func() (string, error) {
			return fmt.Sprintf("scaled %s to %d", row.Name, n), c.Scale(context.Background(), k, row.Namespace, row.Name, int32(n))
		})
	case inForward:
		return m.forward(value)
	case inConfirm:
		p := m.confirm
		m.confirm = nil
		if p == nil {
			return nil
		}
		if value != p.want {
			m.status, m.isErr = "not confirmed — nothing was changed", true
			return nil
		}
		return func() tea.Msg {
			text, err := p.run()
			return doneMsg{text: text, err: err}
		}
	}
	return nil
}

// guard asks before a write: "y" for scale, restart, cordon, drain...
func (m *Model) guard(what string, row kube.ClusterRow, run func() (string, error)) tea.Cmd {
	want := "y"
	prompt := fmt.Sprintf("%s on %s? type y: ", what, short(row.Context))
	m.confirm = &pending{what: what, want: want, run: run}
	return m.ask(inConfirm, prompt, "")
}

// guardNamed is for delete and apply: the resource's name has to be typed.
func (m *Model) guardNamed(what string, row kube.ClusterRow, run func() (string, error)) tea.Cmd {
	m.confirm = &pending{what: what, want: row.Name, run: run}
	return m.ask(inConfirm, fmt.Sprintf("%s on %s? Type %s and confirm: ", what, short(row.Context), row.Name), "")
}

func (m *Model) tableKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := max(1, m.tableHeight()-1)
	switch k.String() {
	case "q":
		m.stopLogs()
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "pgdown", "ctrl+f":
		m.move(page)
	case "pgup", "ctrl+b":
		m.move(-page)
	case "g", "home":
		m.cursor = 0
		m.move(0)
	case "G", "end":
		m.cursor = len(m.visible) - 1
		m.move(0)
	case ":":
		return m, m.ask(inCommand, ": ", "")
	case "/":
		return m, m.ask(inFilter, "/ ", m.filter)
	case "esc":
		if len(m.marked) > 0 {
			m.marked = nil
			m.status = "selection cleared"
			return m, nil
		}
		if m.filter != "" {
			m.filter = ""
			m.applyFilter()
		}
	case "?":
		m.showText("Guide", Guide(m.width))
	case "1", "2", "3", "4":
		m.health = map[string]kube.Health{"1": "all", "2": kube.Failing, "3": kube.Warning, "4": kube.Healthy}[k.String()]
		m.cursor, m.offset = 0, 0
		m.applyFilter()
	case "C":
		m.mode = modeContexts
		m.ctxCursor = 0
		for i, c := range m.pickerContexts() {
			if c.Name == m.ctxName {
				m.ctxCursor = i
			}
		}
	case "R", "ctrl+r":
		return m, m.reload()
	case "0":
		m.namespace = ""
		return m, m.reload()
	case "n":
		if row, _, ok := m.selected(); ok && row.Namespace != "" {
			m.namespace = row.Namespace
			m.status = "namespace " + row.Namespace + " (0 for all)"
			return m, m.reload()
		}
	case "enter", "y":
		return m, m.yaml()
	case "x":
		return m, m.explain()
	case "v":
		return m, m.values()
	case "l":
		if m.kind.ID() == "pods" && len(m.marked) > 1 {
			var pods []kube.ClusterRow
			for _, r := range m.marked {
				pods = append(pods, r)
			}
			sort.Slice(pods, func(i, j int) bool { return markKey(pods[i]) < markKey(pods[j]) })
			return m, m.mergedLogs(pods, fmt.Sprintf("%d marked pods", len(pods)))
		}
		if hasPods(m.kind) {
			return m, m.workloadLogs()
		}
		return m, m.openLogs(false)
	case "p":
		return m, m.openLogs(true)
	case "s":
		return m, m.shell()
	case "e":
		return m, m.edit()
	case "ctrl+d":
		if len(m.marked) > 0 {
			return m, m.deleteMarked()
		}
		return m, m.deleteSelected()
	case " ":
		if row, _, ok := m.selected(); ok {
			k := markKey(row)
			if _, on := m.marked[k]; on {
				delete(m.marked, k)
			} else {
				if m.marked == nil {
					m.marked = map[string]kube.ClusterRow{}
				}
				m.marked[k] = row
			}
			m.move(1)
		}
	case "ctrl+a":
		if m.marked == nil {
			m.marked = map[string]kube.ClusterRow{}
		}
		for n, i := range m.visible {
			if n >= maxBulk {
				m.status, m.isErr = fmt.Sprintf("marked the first %d; narrow the filter for the rest", maxBulk), false
				break
			}
			m.marked[markKey(m.rows[i])] = m.rows[i]
		}
	case "S":
		if row, _, ok := m.selected(); ok {
			if !scalable(m.kind) {
				m.status, m.isErr = m.kind.ID()+" cannot be scaled", true
				return m, nil
			}
			return m, m.ask(inScale, "replicas for "+row.Name+": ", strings.SplitN(row.Cells["READY"]+"/", "/", 2)[1])
		}
	case "r":
		return m, m.restart()
	case "f":
		if row, _, ok := m.selected(); ok && m.kind.ID() == "pods" {
			return m, m.ask(inForward, "forward local:remote for "+row.Name+": ", "0:8080")
		}
	case "c":
		return m, m.cordon()
	case "D":
		return m, m.drain()
	case "N":
		return m, m.createCmd(nil)
	case "T":
		return m, m.topologyCmd(nil)
	case "H":
		return m, m.helmCmd(nil)
	}
	return m, nil
}

func (m *Model) command(line string) tea.Cmd {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	cmd, args := strings.ToLower(fields[0]), fields[1:]
	switch cmd {
	case "q", "quit", "exit":
		m.stopLogs()
		return tea.Quit
	case "ctx", "context":
		if len(args) == 0 {
			m.mode = modeContexts
			return nil
		}
		name, err := m.findContext(args[0])
		if err != nil {
			m.setErr(err)
			return nil
		}
		return m.connect(name, m.kind.ID())
	case "ns", "namespace":
		m.namespace = ""
		if len(args) > 0 && args[0] != "all" {
			m.namespace = args[0]
		}
		return m.reload()
	case "all", "everywhere":
		// :all pods [!ok] -- one kind across every context
		if len(args) == 0 {
			m.status, m.isErr = "usage: all <kind> [filter]", true
			return nil
		}
		if m.cluster == nil {
			return nil
		}
		k, err := m.cluster.ResolveKind(args[0])
		if err != nil {
			m.setErr(err)
			return nil
		}
		m.kind, m.everywhere, m.marked = k, true, nil
		m.filter = strings.Join(args[1:], " ")
		m.cursor, m.offset = 0, 0
		m.status = "listing " + k.ID() + " in every context…"
		return m.reload()
	case "pf", "forwards":
		var b strings.Builder
		for _, f := range kube.Forwards() {
			fmt.Fprintf(&b, "127.0.0.1:%d -> %s/%s:%d   (%s)\n", f.Local, f.Namespace, f.Pod, f.Remote, short(f.Context))
		}
		if b.Len() == 0 {
			b.WriteString("no port-forwards running")
		}
		m.showText("port-forwards", b.String())
		return nil
	case "audit":
		data, err := os.ReadFile(kube.AuditPath())
		if err != nil {
			m.setErr(err)
			return nil
		}
		m.showText("audit log ("+kube.AuditPath()+")", string(data))
		m.view.GotoBottom()
		return nil
	case "help", "h", "?":
		m.showText("Guide", Guide(m.width))
		return nil
	case "overview", "everything", "get-all", "getall":
		// :everything <ns> -- every kind with something in the namespace, like
		// `kubectl get all -n ns` but complete.
		if m.cluster == nil {
			return nil
		}
		ns := m.namespace
		if len(args) > 0 {
			ns = args[0]
		}
		if ns == "" {
			m.status, m.isErr = "usage: overview <namespace>   (or pick one first with :ns)", true
			return nil
		}
		c := m.cluster
		m.status, m.isErr = "loading "+ns+"…", false
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := c.Everything(ctx, ns, false)
			if err != nil {
				return textMsg{err: err}
			}
			return textMsg{title: "Namespace Overview · " + ns, body: everythingText(res)}
		}
	case "helm":
		return m.helmCmd(args)
	case "cost", "costs":
		return m.costCmd(args)
	case "argo", "argocd":
		return m.argoCmd()
	case "topology", "topo", "map":
		return m.topologyCmd(args)
	case "create", "new":
		return m.createCmd(args)
	case "cp", "copy":
		if len(args) > 0 && (args[0] == "put" || args[0] == "up" || args[0] == "upload") {
			return m.cpCmd("put", args[1:])
		}
		return m.cpCmd("cp", args)
	case "ls", "files":
		return m.cpCmd("ls", args)
	case "logs":
		if row, _, ok := m.selected(); ok && hasPods(m.kind) {
			_ = row
			return m.workloadLogs()
		}
		return m.openLogs(false)
	case "kinds":
		if m.cluster == nil {
			return nil
		}
		kinds, err := m.cluster.Kinds()
		if err != nil {
			m.setErr(err)
			return nil
		}
		m.showText(fmt.Sprintf("Resource types (%d)", len(kinds)), kindsText(kinds, strings.Join(args, " ")))
		return nil
	}
	// Anything else is a resource type: "po", "deploy", "app", "nodepools"...
	if m.cluster == nil {
		return nil
	}
	k, err := m.cluster.ResolveKind(cmd)
	if err != nil {
		m.setErr(err)
		return nil
	}
	m.kind, m.everywhere, m.marked = k, false, nil
	m.filter = strings.Join(args, " ")
	m.cursor, m.offset = 0, 0
	return m.reload()
}

func (m *Model) findContext(q string) (string, error) {
	var hits []string
	for _, c := range m.mgr.Contexts() {
		if c.Name == q || short(c.Name) == q {
			return c.Name, nil
		}
		if strings.Contains(strings.ToLower(c.Name), strings.ToLower(q)) {
			hits = append(hits, c.Name)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("no context matches %q", q)
	}
	return "", fmt.Errorf("%q matches %d contexts: %s", q, len(hits), strings.Join(shortAll(hits), ", "))
}

func (m *Model) pickerContexts() []kube.ContextInfo {
	all := m.mgr.AllContexts()
	sort.SliceStable(all, func(i, j int) bool { return !all[i].Hidden && all[j].Hidden })
	return all
}

func (m *Model) contextKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	ctxs := m.pickerContexts()
	switch k.String() {
	case "esc", "q":
		m.mode = modeTable
	case "j", "down":
		m.ctxCursor = min(len(ctxs)-1, m.ctxCursor+1)
	case "k", "up":
		m.ctxCursor = max(0, m.ctxCursor-1)
	case "h":
		if m.ctxCursor < len(ctxs) {
			c := ctxs[m.ctxCursor]
			if c.Current && !c.Hidden {
				m.status, m.isErr = "kubectl's current context stays in the list", true
				return m, nil
			}
			if err := m.mgr.SetHidden(c.Name, !c.Hidden); err != nil {
				m.setErr(err)
				return m, nil
			}
			if c.Hidden {
				m.status, m.isErr = short(c.Name)+" is back in the list", false
			} else {
				m.status, m.isErr = short(c.Name)+" hidden; kubectl still has it. h again to show it", false
			}
		}
	case "d":
		if m.ctxCursor < len(ctxs) {
			c := ctxs[m.ctxCursor]
			if c.Current {
				m.status, m.isErr = "kubectl's current context can't be deleted; switch kubectl to another one first", true
				return m, nil
			}
			want := short(c.Name)
			m.confirm = &pending{what: "delete-context", want: want, run: func() (string, error) {
				backup, err := m.mgr.DeleteFromKubeconfig(c.Name)
				if err != nil {
					return "", err
				}
				return "deleted " + want + " from your kubeconfig; backup " + backup, nil
			}}
			return m, m.ask(inConfirm, fmt.Sprintf("Delete %s from your kubeconfig? kubectl loses it too (backup kept). Type %q: ", want, want), "")
		}
	case "enter":
		m.mode = modeTable
		if m.ctxCursor < len(ctxs) {
			kind := m.kind.ID()
			if kind == "" {
				kind = "pods"
			}
			return m, m.connect(ctxs[m.ctxCursor].Name, kind)
		}
	}
	return m, nil
}

func (m *Model) textKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = modeTable
		return m, nil
	}
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(k)
	return m, cmd
}

func (m *Model) logKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.stopLogs()
		m.mode = modeTable
		m.status = ""
		return m, nil
	case "F":
		m.logFollow = !m.logFollow
		if m.logFollow {
			m.view.GotoBottom()
		}
		return m, nil
	case "/":
		return m, m.ask(inLogFind, "find: ", m.logFind)
	case "P":
		if m.merged {
			return m, nil
		}
		m.previous = !m.previous
		return m, m.startLogs()
	case "c":
		if !m.merged && len(m.containers) > 1 {
			m.container = (m.container + 1) % len(m.containers)
			return m, m.startLogs()
		}
		return m, nil
	case "w":
		return m, m.saveLogs()
	}
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(k)
	m.logFollow = m.view.AtBottom()
	return m, cmd
}

// ---------------------------------------------------------------- actions

func (m *Model) selected() (kube.ClusterRow, *kube.Cluster, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return kube.ClusterRow{}, nil, false
	}
	row := m.rows[m.visible[m.cursor]]
	c, err := m.mgr.Cluster(row.Context)
	if err != nil {
		m.setErr(err)
		return row, nil, false
	}
	return row, c, true
}

func (m *Model) yaml() tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	k := m.kind
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		body, err := c.YAML(ctx, k, row.Namespace, row.Name)
		return textMsg{title: k.Kind + " " + row.Namespace + "/" + row.Name + " — e to edit", body: body, err: err}
	}
}

func (m *Model) explain() tea.Cmd {
	row, c, ok := m.selected()
	if !ok || m.kind.ID() != "pods" {
		m.status, m.isErr = "x explains pods", true
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		why, err := c.Explain(ctx, row.Namespace, row.Name)
		return textMsg{title: "why: " + row.Namespace + "/" + row.Name, body: "• " + strings.Join(why, "\n\n• "), err: err}
	}
}

// values shows a Secret's decoded keys (or a ConfigMap's data). Reading needs no
// confirmation; every reveal of a Secret is in the audit log.
func (m *Model) values() tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	id := m.kind.ID()
	if id != "secrets" && id != "configmaps" {
		m.status, m.isErr = "v shows the values of secrets and configmaps", true
		return nil
	}
	show := func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var entries []kube.SecretEntry
		var err error
		if id == "secrets" {
			entries, err = c.SecretData(ctx, row.Namespace, row.Name)
		} else {
			entries, err = c.ConfigMapData(ctx, row.Namespace, row.Name)
		}
		if err != nil {
			return "", err
		}
		return entriesText(entries), nil
	}
	title := "Values  " + row.Namespace + "/" + row.Name
	return func() tea.Msg {
		body, err := show()
		return textMsg{title: title, body: body, err: err}
	}
}

func entriesText(entries []kube.SecretEntry) string {
	if len(entries) == 0 {
		return stDim.Render("No data keys.")
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(stSection.Render(e.Key) + stFaint.Render(fmt.Sprintf("  %d bytes", e.Size)) + "\n")
		if e.Binary {
			b.WriteString(stDim.Render("  binary value, not shown") + "\n")
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(e.Value, "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m *Model) deleteSelected() tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	k := m.kind
	return m.guardNamed(fmt.Sprintf("delete %s %s/%s", k.Kind, row.Namespace, row.Name), row, func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return "deleted " + row.Name, c.Delete(ctx, k, row.Namespace, row.Name)
	})
}

const maxBulk = 100

func markKey(r kube.ClusterRow) string { return r.Context + "\x00" + r.Namespace + "\x00" + r.Name }

// deleteMarked deletes every marked row. With a protected cluster among them the
// confirmation is "delete N" for the whole batch rather than N names.
func (m *Model) deleteMarked() tea.Cmd {
	rows := make([]kube.ClusterRow, 0, len(m.marked))
	for _, r := range m.marked {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return markKey(rows[i]) < markKey(rows[j]) })
	if len(rows) > maxBulk {
		m.status, m.isErr = fmt.Sprintf("delete at most %d at a time", maxBulk), true
		return nil
	}
	protected := false
	for _, r := range rows {
		if m.mgr.Protected(r.Context) {
			protected = true
		}
	}
	k := m.kind
	run := func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		done, failed := 0, []string{}
		for _, r := range rows {
			c, err := m.mgr.Cluster(r.Context)
			if err == nil {
				err = c.Delete(ctx, k, r.Namespace, r.Name)
			}
			if err != nil {
				failed = append(failed, r.Name+": "+firstLine(err.Error()))
				continue
			}
			done++
		}
		m.marked = nil
		if len(failed) > 0 {
			return "", fmt.Errorf("deleted %d of %d; %d failed, first: %s", done, len(rows), len(failed), failed[0])
		}
		return fmt.Sprintf("deleted %d %s", done, k.Name), nil
	}
	phrase := "delete"
	want, prompt := "y", fmt.Sprintf("delete %d marked %s? type y: ", len(rows), k.Name)
	if protected {
		want = phrase
		prompt = fmt.Sprintf("Delete %d %s? Type %q to confirm: ", len(rows), k.Name, phrase)
	}
	m.confirm = &pending{what: phrase, want: want, run: run}
	return m.ask(inConfirm, prompt, "")
}

func (m *Model) restart() tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	k := m.kind
	if k.ID() == "pods" {
		// A pod restarts by being deleted: its controller makes a new one.
		return m.guardNamed(fmt.Sprintf("restart pod %s/%s (delete it; its controller starts a new one)", row.Namespace, row.Name), row, func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return "restarted " + row.Name, c.Delete(ctx, k, row.Namespace, row.Name)
		})
	}
	switch k.ID() {
	case "deployments.apps", "statefulsets.apps", "daemonsets.apps":
	default:
		m.status, m.isErr = "r restarts pods, deployments, statefulsets and daemonsets", true
		return nil
	}
	return m.guard(fmt.Sprintf("rollout restart %s/%s", row.Namespace, row.Name), row, func() (string, error) {
		return "restarted " + row.Name, c.Restart(context.Background(), k, row.Namespace, row.Name)
	})
}

func (m *Model) cordon() tea.Cmd {
	row, c, ok := m.selected()
	if !ok || m.kind.ID() != "nodes" {
		return nil
	}
	on := !strings.Contains(row.Status, "SchedulingDisabled")
	verb := map[bool]string{true: "cordon", false: "uncordon"}[on]
	return m.guard(verb+" node "+row.Name, row, func() (string, error) {
		return verb + "ed " + row.Name, c.Cordon(context.Background(), row.Name, on)
	})
}

func (m *Model) drain() tea.Cmd {
	row, c, ok := m.selected()
	if !ok || m.kind.ID() != "nodes" {
		return nil
	}
	return m.guard("drain node "+row.Name+" (evicts every non-DaemonSet pod)", row, func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		failed, err := c.Drain(ctx, row.Name)
		if err == nil && len(failed) > 0 {
			return fmt.Sprintf("drained %s; %d pods not evicted (PDB?): %s", row.Name, len(failed), failed[0]), nil
		}
		return "drained " + row.Name, err
	})
}

func (m *Model) forward(spec string) tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	l, r, found := strings.Cut(spec, ":")
	local, err1 := strconv.Atoi(l)
	remote, err2 := strconv.Atoi(r)
	if !found || err1 != nil || err2 != nil {
		m.status, m.isErr = "format is local:remote, e.g. 0:8080 (0 picks a free port)", true
		return nil
	}
	return func() tea.Msg {
		f, err := c.PortForward(row.Namespace, row.Name, local, remote)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: fmt.Sprintf("forwarding 127.0.0.1:%d -> %s:%d  (:pf to list)", f.Local, row.Name, f.Remote)}
	}
}

// edit opens the object in $EDITOR and applies it on save, after confirmation.
func (m *Model) edit() tea.Cmd {
	row, c, ok := m.selected()
	if !ok {
		return nil
	}
	body, err := c.YAML(context.Background(), m.kind, row.Namespace, row.Name)
	if err != nil {
		m.setErr(err)
		return nil
	}
	f, err := os.CreateTemp("", "kv-*-"+row.Name+".yaml")
	if err != nil {
		m.setErr(err)
		return nil
	}
	_, _ = f.WriteString(body)
	f.Close()
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
	cmd := exec.Command(parts[0], append(parts[1:], f.Name())...)
	path := f.Name()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editedMsg{path: path, original: body, row: row, err: err}
	})
}

func (m *Model) afterEdit(msg editedMsg) tea.Cmd {
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
	if string(data) == msg.original {
		m.status, m.isErr = "no changes", false
		return nil
	}
	c, err := m.mgr.Cluster(msg.row.Context)
	if err != nil {
		m.setErr(err)
		return nil
	}
	k, text := m.kind, string(data)
	return m.guardNamed(fmt.Sprintf("apply edited %s/%s", msg.row.Namespace, msg.row.Name), msg.row, func() (string, error) {
		return "applied " + msg.row.Name, c.Apply(context.Background(), k, msg.row.Namespace, msg.row.Name, text)
	})
}

func (m *Model) shell() tea.Cmd {
	row, c, ok := m.selected()
	if ok && m.kind.ID() == "nodes" {
		return m.nodeShell(row, c)
	}
	if !ok || m.kind.ID() != "pods" {
		m.status, m.isErr = "s opens a shell in a pod or on a node", true
		return nil
	}
	containers, err := c.Containers(context.Background(), row.Namespace, row.Name)
	if err != nil {
		m.setErr(err)
		return nil
	}
	container := containers[len(containers)-1]
	return tea.Exec(&shellCmd{c: c, ns: row.Namespace, pod: row.Name, container: container}, func(err error) tea.Msg {
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: "shell in " + row.Name + " closed"}
	})
}

// nodeShell starts a helper pod on the node, opens a root shell on the host through
// it, and removes the pod when the shell exits.
func (m *Model) nodeShell(row kube.ClusterRow, c *kube.Cluster) tea.Cmd {
	m.status, m.isErr = "starting a shell pod on "+row.Name+"…", false
	node := row.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ns, pod, err := c.StartNodeShell(ctx, node)
		if err != nil {
			return doneMsg{err: err}
		}
		return nodeShellReady{c: c, node: node, ns: ns, pod: pod}
	}
}

type nodeShellReady struct {
	c             *kube.Cluster
	node, ns, pod string
}

// ---------------------------------------------------------------- logs

func (m *Model) openLogs(previous bool) tea.Cmd {
	row, c, ok := m.selected()
	if !ok || m.kind.ID() != "pods" {
		m.status, m.isErr = "l shows logs for pods", true
		return nil
	}
	containers, err := c.Containers(context.Background(), row.Namespace, row.Name)
	if err != nil {
		m.setErr(err)
		return nil
	}
	m.logPod, m.containers, m.previous, m.merged = row, containers, previous, false
	m.container = len(containers) - 1 // the app container; init containers come first
	m.mode = modeLogs
	return m.startLogs()
}

func (m *Model) startLogs() tea.Cmd {
	m.stopLogs()
	c, err := m.mgr.Cluster(m.logPod.Context)
	if err != nil {
		m.setErr(err)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan string, 1024)
	m.logCancel, m.logCh, m.logLines, m.logFollow = cancel, ch, nil, true
	opts := kube.LogOptions{Container: m.containers[m.container], Follow: !m.previous, Previous: m.previous, TailLines: 1000}
	pod := m.logPod
	errc := make(chan error, 1)
	go func() { errc <- c.Logs(ctx, pod.Namespace, pod.Name, opts, ch) }()
	m.renderLogs()
	return tea.Batch(m.waitLog(), func() tea.Msg {
		if err := <-errc; err != nil {
			return logEndMsg{err: err}
		}
		return nil
	})
}

// waitLog reads the next batch of lines. Batching matters: a busy pod logs thousands
// of lines a second and a re-render per line would freeze the terminal.
func (m *Model) waitLog() tea.Cmd {
	ch := m.logCh
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return logEndMsg{}
		}
		lines := []string{line}
		for len(lines) < 500 {
			select {
			case l, ok := <-ch:
				if !ok {
					return logMsg{lines: lines}
				}
				lines = append(lines, l)
			default:
				return logMsg{lines: lines}
			}
		}
		return logMsg{lines: lines}
	}
}

func (m *Model) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel, m.logCh = nil, nil
	}
}

func (m *Model) renderLogs() {
	lines := m.logLines
	if m.logFind != "" {
		var hits []string
		q := strings.ToLower(m.logFind)
		for _, l := range lines {
			if strings.Contains(strings.ToLower(stripANSI(l)), q) {
				hits = append(hits, l)
			}
		}
		lines = hits
	}
	col := make([]string, len(lines))
	for i, l := range lines {
		if !m.merged {
			l = colorLog(l)
		}
		col[i] = truncate(l, max(20, m.width))
	}
	lines = col
	m.view.SetContent(strings.Join(lines, "\n"))
	if m.logFollow {
		m.view.GotoBottom()
	}
}

func (m *Model) saveLogs() tea.Cmd {
	name := safeName(fmt.Sprintf("%s_%s_%s.log", strings.ReplaceAll(m.logPod.Name, " ", "-"), strings.ReplaceAll(m.containers[m.container], " ", "-"), time.Now().Format("20060102-150405")))
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "Downloads", name)
	if err := os.WriteFile(path, []byte(stripANSI(strings.Join(m.logLines, "\n"))+"\n"), 0o644); err != nil {
		m.setErr(err)
		return nil
	}
	m.status, m.isErr = "saved "+path, false
	return nil
}

// ---------------------------------------------------------------- table

func (m *Model) applyFilter() {
	f := kube.ParseFilter(m.filter)
	m.filtered, m.visible = m.filtered[:0], m.visible[:0]
	for i, r := range m.rows {
		if f.Match(r.Context, r.Row) {
			m.filtered = append(m.filtered, i)
			if m.health == "all" || r.Health == m.health {
				m.visible = append(m.visible, i)
			}
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
	m.move(0)
}

func (m *Model) move(d int) {
	m.cursor = clamp(m.cursor+d, 0, max(0, len(m.visible)-1))
	h := m.tableHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m *Model) tableHeight() int {
	if m.pageH > 0 {
		return m.pageH
	}
	return max(1, m.height-5)
}

func (m *Model) showText(title, body string) {
	m.mode = modeText
	m.viewTitle = title
	m.view.SetContent(body)
	m.view.GotoTop()
}

func scalable(k kube.Kind) bool {
	for _, v := range []string{"deployments.apps", "statefulsets.apps", "replicasets.apps"} {
		if k.ID() == v {
			return true
		}
	}
	return false
}

func short(ctx string) string {
	if i := strings.LastIndex(ctx, "/"); i >= 0 {
		return ctx[i+1:]
	}
	return ctx
}

func shortAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = short(s)
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }
