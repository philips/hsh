package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"hsh/internal/denieddb"
	"hsh/internal/tui/proxywatch"
)

func ProxyWatch(args []string) error {
	fs := flag.NewFlagSet("proxy-watch", flag.ContinueOnError)
	configPath := fs.String("config", "config.hsh", "config file to add allowed hosts to")
	dbPath := fs.String("db", denieddb.DefaultPath(), "denial database path")
	all := fs.Bool("all", false, "show ignored hosts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("proxy-watch requires a terminal; use 'hsh proxy-denied' for a plain list")
	}
	if !denieddb.Exists(*dbPath) {
		fmt.Println("no denied hosts recorded")
		return nil
	}
	db, err := denieddb.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	pending, err := denieddb.Pending(db)
	if err != nil {
		return err
	}
	var ignored []denieddb.DeniedHost
	if *all {
		ignored, err = denieddb.Ignored(db)
		if err != nil {
			return err
		}
	}
	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	model := proxywatch.New(db, absConfig, pending, ignored, *all)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		return err
	}
	return nil
}
