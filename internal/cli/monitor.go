package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/crashlog"
	"hsh/internal/monitordb"
	"hsh/internal/settings"
	"hsh/internal/tui/palette"
)

func monitorCommand() *ff.Command {
	var all bool
	var filter monitordb.Filter
	return newCommand("monitor", "hsh monitor [FLAGS]", "command palette: review denied hosts, add allowlist entries, change settings (TUI)", func(fs *ff.FlagSet) {
		fs.BoolVar(&all, 0, "all", "show ignored hosts")
		sessionFilterFlags(fs, &filter)
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("monitor takes no positional arguments")
		}
		if err := resolveFilterSession(&filter); err != nil {
			return err
		}
		if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return errors.New("monitor requires a terminal; use 'hsh proxy denied' for a plain list")
		}
		key, warn := effectiveMonitorKey(config.DefaultMonitorKey)
		if warn != "" {
			fmt.Fprintln(os.Stderr, warn)
		}
		opts := palette.Options{Filter: filter, ShowIgnored: all, SettingsPath: settings.Path(), MonitorKey: key, Sessions: monitorSessionOps(), Forwards: monitorForwardOps()}
		store := monitordb.DefaultStore()
		var err error
		if opts.Pending, err = store.Pending(filter); err != nil {
			return err
		}
		if all {
			if opts.Ignored, err = store.Ignored(filter); err != nil {
				return err
			}
		}
		opts.DB = store
		defer watchSessions(filter)()
		if _, err := tea.NewProgram(crashlog.Guard("hsh monitor", palette.New(opts)), tea.WithAltScreen()).Run(); err != nil {
			if errors.Is(err, tea.ErrProgramPanic) {
				return fmt.Errorf("the monitor crashed; details in %s", crashlog.Path())
			}
			return err
		}
		return nil
	})
}
