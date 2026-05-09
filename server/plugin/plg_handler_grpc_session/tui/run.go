package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

func Run(ctx context.Context, opts AppOptions) error {
	if opts.Context == nil {
		opts.Context = ctx
	}
	_, err := tea.NewProgram(NewModel(opts), tea.WithContext(opts.Context)).Run()
	return err
}
