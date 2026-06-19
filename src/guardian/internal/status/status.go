package status

import (
	"fmt"
	"io"
	"strings"
	"sync"

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
}

type Renderer struct {
	mu          sync.Mutex
	w           io.Writer
	mode        Mode
	clusterName string
	events      map[string]up.StatusEvent
	order       []string
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
		events:      map[string]up.StatusEvent{},
	}
	if opts.Mode == ModeTUI {
		r.done = make(chan struct{})
		r.program = tea.NewProgram(
			newDashboardModel(opts.ClusterName),
			tea.WithInput(nil),
			tea.WithOutput(w),
			tea.WithoutSignalHandler(),
		)
		go func() {
			_, r.runErr = r.program.Run()
			close(r.done)
		}()
	}
	return r
}

func (r *Renderer) Report(event up.StatusEvent) {
	if event.Name == "" {
		return
	}
	if r.mode == ModeTUI {
		r.program.Send(statusMsg{event: event})
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.events[event.Name]; !ok {
		r.order = append(r.order, event.Name)
	}
	r.events[event.Name] = event
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
	switch event.State {
	case up.StatusRunning:
		fmt.Fprintf(r.w, "-----> %s\n", event.Title)
		if event.Description != "" {
			fmt.Fprintf(r.w, "       %s\n", event.Description)
		}
	case up.StatusDone:
		fmt.Fprintf(r.w, "       done %s\n", event.Title)
	case up.StatusSkipped:
		fmt.Fprintf(r.w, "       skipped %s", event.Title)
		if event.Detail != "" {
			fmt.Fprintf(r.w, ": %s", event.Detail)
		}
		fmt.Fprintln(r.w)
	case up.StatusFailed:
		fmt.Fprintf(r.w, " !     failed %s", event.Title)
		if event.Detail != "" {
			fmt.Fprintf(r.w, ": %s", event.Detail)
		}
		fmt.Fprintln(r.w)
	}
}

type statusMsg struct {
	event up.StatusEvent
}

type dashboardModel struct {
	clusterName string
	events      map[string]up.StatusEvent
	order       []string
	active      string
	width       int
	height      int
}

func newDashboardModel(clusterName string) dashboardModel {
	return dashboardModel{
		clusterName: clusterName,
		events:      map[string]up.StatusEvent{},
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return nil
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case statusMsg:
		event := msg.event
		if _, ok := m.events[event.Name]; !ok {
			m.order = append(m.order, event.Name)
		}
		m.events[event.Name] = event
		if event.State == up.StatusRunning || event.State == up.StatusFailed {
			m.active = event.Name
		}
		if event.State == up.StatusDone || event.State == up.StatusSkipped {
			if m.active == event.Name {
				m.active = ""
			}
		}
	}
	return m, nil
}

func (m dashboardModel) View() string {
	width := m.width
	if width < 80 {
		width = 80
	}
	if width > 120 {
		width = 120
	}

	var b strings.Builder
	b.WriteString(headerStyle(width).Render("Guardian up"))
	b.WriteString("\n")
	b.WriteString(subtleStyle.Render(m.clusterName + "  host come-up: Talos -> Kubernetes -> Cozystack"))
	b.WriteString("\n\n")
	b.WriteString(m.summaryView(width))
	b.WriteString("\n\n")
	b.WriteString(m.activeView(width))
	b.WriteString("\n\n")
	b.WriteString(m.stepsView(width))
	return b.String()
}

func (m dashboardModel) summaryView(width int) string {
	var done, running, skipped, failed int
	for _, event := range m.events {
		switch event.State {
		case up.StatusDone:
			done++
		case up.StatusRunning:
			running++
		case up.StatusSkipped:
			skipped++
		case up.StatusFailed:
			failed++
		}
	}
	total := len(m.order)
	cells := []string{
		statCell("done", done),
		statCell("active", running),
		statCell("skipped", skipped),
		statCell("failed", failed),
		statCell("seen", total),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

func (m dashboardModel) activeView(width int) string {
	event, ok := m.events[m.active]
	if !ok {
		if len(m.order) == 0 {
			return panel(width, "ACTIVE", "Waiting for the first bootstrap event.")
		}
		last := m.events[m.order[len(m.order)-1]]
		return panel(width, "ACTIVE", fmt.Sprintf("%s\n%s", stateLabel(last.State), last.Title))
	}
	description := event.Description
	if event.State == up.StatusFailed && event.Detail != "" {
		description = event.Detail
	}
	return panel(width, "ACTIVE", fmt.Sprintf("%s\n%s", event.Title, description))
}

func (m dashboardModel) stepsView(width int) string {
	visible := m.order
	maxRows := 14
	if m.height > 0 {
		maxRows = m.height - 12
		if maxRows < 6 {
			maxRows = 6
		}
	}
	if len(visible) > maxRows {
		visible = visible[len(visible)-maxRows:]
	}
	rows := make([]string, 0, len(visible)+1)
	rows = append(rows, sectionTitle.Render("STEPS"))
	for _, name := range visible {
		event := m.events[name]
		rows = append(rows, stepLine(event, width))
	}
	return strings.Join(rows, "\n")
}

func headerStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("30")).
		Padding(0, 1).
		Width(width)
}

var (
	subtleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	sectionTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	panelStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
)

func statCell(label string, value int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(14).
		Render(fmt.Sprintf("%s\n%d", subtleStyle.Render(label), value))
}

func panel(width int, title, body string) string {
	innerWidth := width - 4
	if innerWidth < 20 {
		innerWidth = 20
	}
	content := sectionTitle.Render(title) + "\n" + strings.TrimSpace(body)
	return panelStyle.Width(innerWidth).Render(content)
}

func stepLine(event up.StatusEvent, width int) string {
	label := stateLabel(event.State)
	titleWidth := 30
	descriptionWidth := width - titleWidth - 18
	if descriptionWidth < 20 {
		descriptionWidth = 20
	}
	description := event.Description
	if event.State == up.StatusFailed && event.Detail != "" {
		description = event.Detail
	}
	if event.State == up.StatusSkipped && event.Detail != "" {
		description = event.Detail
	}
	prefix := "      "
	if event.Total > 0 {
		prefix = fmt.Sprintf("[%02d/%02d]", event.Index, event.Total)
	}
	return fmt.Sprintf(
		"%s %s %-*s %s",
		subtleStyle.Render(prefix),
		label,
		titleWidth,
		truncate(event.Title, titleWidth),
		subtleStyle.Render(truncate(description, descriptionWidth)),
	)
}

func stateLabel(state up.StatusState) string {
	switch state {
	case up.StatusRunning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("RUN ")
	case up.StatusDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("OK  ")
	case up.StatusSkipped:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("SKIP")
	case up.StatusFailed:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true).Render("FAIL")
	default:
		return "    "
	}
}

func truncate(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= width {
		return value
	}
	if width <= 1 {
		return value[:width]
	}
	return value[:width-1] + "."
}
