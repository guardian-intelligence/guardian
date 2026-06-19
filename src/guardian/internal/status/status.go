package status

import (
	"fmt"
	"io"
	"strings"
	"sync"

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
	lines       int
	started     bool
}

func New(w io.Writer, opts Options) *Renderer {
	return &Renderer{
		w:           w,
		mode:        opts.Mode,
		clusterName: opts.ClusterName,
		events:      map[string]up.StatusEvent{},
	}
}

func (r *Renderer) Report(event up.StatusEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if event.Name == "" {
		return
	}
	if _, ok := r.events[event.Name]; !ok {
		r.order = append(r.order, event.Name)
	}
	r.events[event.Name] = event

	if r.mode == ModeTUI {
		r.renderTUI()
		return
	}
	r.renderPlain(event)
}

func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mode == ModeTUI && r.lines > 0 {
		_, err := fmt.Fprintln(r.w)
		return err
	}
	return nil
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

func (r *Renderer) renderTUI() {
	lines := []string{
		fmt.Sprintf("guardian up %s", r.clusterName),
		"",
	}
	for _, name := range r.order {
		event := r.events[name]
		lines = append(lines, renderLine(event))
	}
	if r.lines > 0 {
		fmt.Fprintf(r.w, "\x1b[%dF", r.lines)
	}
	for _, line := range lines {
		fmt.Fprintf(r.w, "\x1b[2K%s\n", line)
	}
	r.lines = len(lines)
	r.started = true
}

func renderLine(event up.StatusEvent) string {
	prefix := "      "
	if event.Total > 0 {
		prefix = fmt.Sprintf("[%02d/%02d]", event.Index, event.Total)
	}
	state := string(event.State)
	if event.State == up.StatusRunning {
		state = "active"
	}
	description := event.Description
	if event.State == up.StatusFailed && event.Detail != "" {
		description = event.Detail
	}
	if event.State == up.StatusSkipped && event.Detail != "" {
		description = event.Detail
	}
	return fmt.Sprintf("%s %-7s %-28s %s", prefix, state, truncate(event.Title, 28), truncate(description, 76))
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
