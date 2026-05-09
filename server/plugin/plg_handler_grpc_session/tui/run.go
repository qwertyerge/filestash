package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

func Run(ctx context.Context, opts AppOptions) error {
	if opts.Context == nil {
		opts.Context = ctx
	}
	model := NewModel(opts)
	defer model.Shutdown(context.Background())
	_, err := tea.NewProgram(model, tea.WithContext(opts.Context)).Run()
	return err
}
