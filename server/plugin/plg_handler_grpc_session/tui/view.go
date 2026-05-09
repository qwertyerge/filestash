package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m Model) View() tea.View {
	return tea.NewView(m.viewString())
}

func (m Model) viewString() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sidecar %s  session %s  backend %s  path %s  mode %s\n",
		m.Connection.Addr,
		emptyDash(m.State.SessionID),
		emptyDash(m.State.BackendType),
		m.State.CurrentPath,
		m.State.EffectiveMode,
	)
	if m.State.Status != "" {
		fmt.Fprintf(&b, "%s\n", m.State.Status)
	}
	b.WriteString("\n")

	if m.Prompt.Kind != PromptNone {
		fmt.Fprintf(&b, "%s: %s", m.Prompt.Label, m.maskedPromptValue())
		return b.String()
	}
	if m.Pager != "" {
		b.WriteString(m.Pager)
		b.WriteString("\n\nesc: close")
		return b.String()
	}

	switch m.State.Phase {
	case PhaseBackendSelect:
		b.WriteString("backend\n")
		for i, backend := range DefaultBackendTypes {
			prefix := "  "
			if i == m.selectedBackend {
				prefix = "> "
			}
			fmt.Fprintf(&b, "%s%s\n", prefix, backend)
		}
		b.WriteString("\nenter: select  w: mode  q: quit")
	case PhaseBrowser, PhaseModal:
		if len(m.Entries) == 0 {
			b.WriteString("(empty)\n")
		}
		for i, entry := range m.Entries {
			prefix := "  "
			if i == m.Cursor {
				prefix = "> "
			}
			fmt.Fprintf(&b, "%s%-32s %-10s %d\n", prefix, entry.GetName(), entry.GetType(), entry.GetSize())
		}
		b.WriteString("\nenter: open  h: parent  r: refresh  i/v: stat/read  u/n/t/R/d: write/mkdir/touch/rename/remove  e/s/S/F/c/q: session")
	default:
		b.WriteString("loading\n")
	}
	return b.String()
}

func (m Model) maskedPromptValue() string {
	if m.Prompt.Kind == PromptParam && IsSensitiveField(m.Prompt.Key) {
		return strings.Repeat("*", len([]rune(m.Prompt.Value)))
	}
	return m.Prompt.Value
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
