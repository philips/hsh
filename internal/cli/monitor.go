package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterbourgon/ff/v4"

	"hsh/internal/denieddb"
	"hsh/internal/tui/proxywatch"
)

func monitorCommand() *ff.Command {
	var dbPath string
	var all bool
	var filter denieddb.Filter
	return newCommand("monitor", "hsh monitor [FLAGS]", "interactively review denied hosts (TUI)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", denieddb.DefaultPath(), "denial database path")
		fs.BoolVar(&all, 0, "all", "show ignored hosts")
		sessionFilterFlags(fs, &filter)
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("monitor takes no positional arguments")
		}
		if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return errors.New("monitor requires a terminal; use 'hsh proxy denied' for a plain list")
		}
		if !denieddb.Exists(dbPath) {
			fmt.Println("no denied hosts recorded")
			return nil
		}
		db, err := denieddb.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		pending, err := denieddb.Pending(db, filter)
		if err != nil {
			return err
		}
		var ignored []denieddb.DeniedHost
		if all {
			ignored, err = denieddb.Ignored(db, filter)
			if err != nil {
				return err
			}
		}
		model := proxywatch.New(db, filter, pending, ignored, all)
		if _, err := tea.NewProgram(model).Run(); err != nil {
			return err
		}
		return nil
	})
}
