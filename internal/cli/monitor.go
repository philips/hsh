package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterbourgon/ff/v4"

	"hsh/internal/monitordb"
	"hsh/internal/tui/proxywatch"
)

func monitorCommand() *ff.Command {
	var dbPath string
	var all bool
	var filter monitordb.Filter
	return newCommand("monitor", "hsh monitor [FLAGS]", "interactively review denied hosts (TUI)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", monitordb.DefaultPath(), "monitor database path")
		fs.BoolVar(&all, 0, "all", "show ignored hosts")
		sessionFilterFlags(fs, &filter)
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("monitor takes no positional arguments")
		}
		if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return errors.New("monitor requires a terminal; use 'hsh proxy denied' for a plain list")
		}
		if !monitordb.Exists(dbPath) {
			fmt.Println("no denied hosts recorded")
			return nil
		}
		db, err := monitordb.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		pending, err := monitordb.Pending(db, filter)
		if err != nil {
			return err
		}
		var ignored []monitordb.DeniedHost
		if all {
			ignored, err = monitordb.Ignored(db, filter)
			if err != nil {
				return err
			}
		}
		defer watchSessions(filter)()
		model := proxywatch.New(db, filter, pending, ignored, all)
		if _, err := tea.NewProgram(model, tea.WithAltScreen()).Run(); err != nil {
			return err
		}
		return nil
	})
}
