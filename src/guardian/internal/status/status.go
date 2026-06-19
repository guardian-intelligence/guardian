package status

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/up"
)

type Mode string

const (
	ModePlain Mode = "plain"
	ModeTUI   Mode = "tui"
)

type Options struct {
	Mode        Mode
	ClusterName string
	Input       io.Reader
}

type Renderer struct {
	mu          sync.Mutex
	w           io.Writer
	mode        Mode
	clusterName string
	started     bool
	program     *tea.Program
	done        chan struct{}
	runErr      error
}

func New(w io.Writer, opts Options) *Renderer {
	r := &Renderer{
		w:           w,
		mode:        opts.Mode,
		clusterName: opts.ClusterName,
	}
	if opts.Mode == ModeTUI {
		programOptions := []tea.ProgramOption{
			tea.WithOutput(w),
			tea.WithoutSignalHandler(),
		}
		if opts.Input != nil {
			programOptions = append(programOptions, tea.WithInput(opts.Input))
		} else {
			programOptions = append(programOptions, tea.WithInput(nil))
		}
		r.done = make(chan struct{})
		r.program = tea.NewProgram(newModel(opts.ClusterName), programOptions...)
		go func() {
			_, r.runErr = r.program.Run()
			close(r.done)
		}()
	}
	return r
}

func (r *Renderer) Report(event up.StatusEvent) {
	if event.ID == "" {
		return
	}
	if r.mode == ModeTUI {
		r.program.Send(statusMsg{event: event})
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.renderPlain(event)
}

func (r *Renderer) Close() error {
	if r.mode != ModeTUI {
		return nil
	}
	r.program.Quit()
	<-r.done
	return r.runErr
}

func (r *Renderer) renderPlain(event up.StatusEvent) {
	if !r.started {
		fmt.Fprintf(r.w, "guardian up %s\n", r.clusterName)
		r.started = true
	}
	icon := plainIcon(event.State)
	fmt.Fprintf(r.w, "%s %s\n", icon, event.Title)
	if event.State == up.StatusFailed || event.State == up.StatusBlocked {
		failure := event.Failure
		if failure == nil {
			return
		}
		if failure.Code != "" {
			fmt.Fprintf(r.w, "  %s\n", failure.Code)
		}
	}
}

func plainIcon(state up.StatusState) string {
	switch state {
	case up.StatusRunning:
		return "/"
	case up.StatusDone:
		return "✓"
	case up.StatusSkipped:
		return "-"
	case up.StatusFailed:
		return "✕"
	case up.StatusBlocked:
		return "!"
	default:
		return "○"
	}
}

type statusMsg struct {
	event up.StatusEvent
}

type tickMsg time.Time

type model struct {
	clusterName string
	nodes       map[string]*node
	rootOrder   []string
	childOrder  map[string][]string
	frame       int
	width       int
	now         time.Time
}

type node struct {
	id          string
	parentID    string
	title       string
	description string
	state       up.StatusState
	failure     *up.StatusFailure
	startedAt   time.Time
	endedAt     time.Time
}

func newModel(clusterName string) model {
	return model{
		clusterName: clusterName,
		nodes:       map[string]*node{},
		childOrder:  map[string][]string{},
		now:         time.Now(),
	}
}

func (m model) Init() tea.Cmd {
	return tick()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tickMsg:
		m.now = time.Time(msg)
		m.frame++
		return m, tick()
	case statusMsg:
		m.apply(msg.event)
	}
	return m, nil
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *model) apply(event up.StatusEvent) {
	if event.ParentID != "" {
		if _, ok := m.nodes[event.ParentID]; !ok {
			m.nodes[event.ParentID] = &node{
				id:    event.ParentID,
				title: event.ParentTitle,
				state: up.StatusPending,
			}
			m.rootOrder = append(m.rootOrder, event.ParentID)
		}
		if !contains(m.childOrder[event.ParentID], event.ID) {
			m.childOrder[event.ParentID] = append(m.childOrder[event.ParentID], event.ID)
		}
	}
	if _, ok := m.nodes[event.ID]; !ok && event.ParentID == "" {
		m.rootOrder = append(m.rootOrder, event.ID)
	}
	n, ok := m.nodes[event.ID]
	if !ok {
		n = &node{id: event.ID}
		m.nodes[event.ID] = n
	}
	n.parentID = event.ParentID
	n.title = event.Title
	n.description = event.Description
	n.state = event.State
	n.failure = event.Failure
	if !event.StartedAt.IsZero() {
		n.startedAt = event.StartedAt
	}
	if !event.EndedAt.IsZero() {
		n.endedAt = event.EndedAt
	}
}

func (m model) View() string {
	width := m.width
	if width < 72 {
		width = 72
	}
	if width > 110 {
		width = 110
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("guardian up"))
	b.WriteString("  ")
	b.WriteString(clusterStyle.Render(m.clusterName))
	b.WriteString("\n\n")
	if len(m.rootOrder) == 0 {
		b.WriteString(m.icon(up.StatusRunning))
		b.WriteString(" Waiting for bootstrap events")
		return b.String()
	}
	for _, id := range m.rootOrder {
		n := m.nodes[id]
		state := m.derivedState(id)
		b.WriteString(m.line(n, state, 0, width))
		b.WriteByte('\n')
		if state == up.StatusRunning || state == up.StatusFailed || state == up.StatusBlocked {
			for _, childID := range m.childOrder[id] {
				child := m.nodes[childID]
				if child.state == up.StatusPending {
					continue
				}
				b.WriteString(m.line(child, child.state, 1, width))
				b.WriteByte('\n')
			}
		}
	}
	if failure := m.firstFailure(); failure != nil {
		b.WriteByte('\n')
		b.WriteString(errorStyle.Render(failure.code()))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) line(n *node, state up.StatusState, depth, width int) string {
	indent := strings.Repeat("  ", depth)
	title := n.title
	if title == "" {
		title = n.id
	}
	line := indent + m.icon(state) + " " + title
	if state == up.StatusRunning && n.description != "" {
		line += "  " + subtleStyle.Render(n.description)
	}
	if d := m.duration(n, state); d != "" {
		padding := width - lipgloss.Width(line) - len(d)
		if padding > 1 {
			line += strings.Repeat(" ", padding)
		} else {
			line += " "
		}
		line += subtleStyle.Render(d)
	}
	return line
}

func (m model) icon(state up.StatusState) string {
	switch state {
	case up.StatusRunning:
		frames := []string{"/", "-", "\\", "|"}
		return runningStyle.Render(frames[m.frame%len(frames)])
	case up.StatusDone:
		return doneStyle.Render("✓")
	case up.StatusSkipped:
		return subtleStyle.Render("-")
	case up.StatusFailed:
		return failedStyle.Render("✕")
	case up.StatusBlocked:
		return blockedStyle.Render("!")
	default:
		return subtleStyle.Render("○")
	}
}

func (m model) duration(n *node, state up.StatusState) string {
	switch state {
	case up.StatusRunning:
		if n.startedAt.IsZero() {
			return ""
		}
		return formatDuration(m.now.Sub(n.startedAt))
	case up.StatusDone, up.StatusSkipped, up.StatusFailed, up.StatusBlocked:
		if n.startedAt.IsZero() || n.endedAt.IsZero() {
			return ""
		}
		return formatDuration(n.endedAt.Sub(n.startedAt))
	default:
		return ""
	}
}

func (m model) derivedState(rootID string) up.StatusState {
	children := m.childOrder[rootID]
	if len(children) == 0 {
		if n := m.nodes[rootID]; n != nil {
			return n.state
		}
		return up.StatusPending
	}
	seen := false
	allDone := true
	for _, childID := range children {
		child := m.nodes[childID]
		if child == nil || child.state == up.StatusPending {
			allDone = false
			continue
		}
		seen = true
		switch child.state {
		case up.StatusFailed:
			return up.StatusFailed
		case up.StatusBlocked:
			return up.StatusBlocked
		case up.StatusRunning:
			return up.StatusRunning
		case up.StatusDone, up.StatusSkipped:
		default:
			allDone = false
		}
	}
	if seen && allDone {
		return up.StatusDone
	}
	if seen {
		return up.StatusRunning
	}
	return up.StatusPending
}

type failureView struct {
	node    *node
	failure *up.StatusFailure
}

func (m model) firstFailure() *failureView {
	for _, rootID := range m.rootOrder {
		if n := m.nodes[rootID]; n != nil && n.failure != nil {
			return &failureView{node: n, failure: n.failure}
		}
		for _, childID := range m.childOrder[rootID] {
			child := m.nodes[childID]
			if child != nil && child.failure != nil {
				return &failureView{node: child, failure: child.failure}
			}
		}
	}
	return nil
}

func (f failureView) code() string {
	if f.failure.Code != "" {
		return f.failure.Code
	}
	if f.node != nil && f.node.id != "" {
		return f.node.id
	}
	return "bootstrap.failed"
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return d.Truncate(100 * time.Millisecond).String()
	}
	if d < time.Minute {
		return d.Truncate(time.Second).String()
	}
	return d.Truncate(time.Second).String()
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var (
	headerStyle     = lipgloss.NewStyle().Bold(true)
	clusterStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	subtleStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	doneStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	runningStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	failedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	blockedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	errorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
)
