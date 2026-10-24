package cli

import (
	"flag"
	"fmt"
	"path/filepath"

	"harnessh/internal/config"
	"harnessh/internal/denieddb"
)

func ProxyAutoAllow(args []string) error {
	fs := flag.NewFlagSet("proxy-auto-allow", flag.ContinueOnError)
	configPath := fs.String("config", "config.harnessh", "config file to add allowed hosts to")
	dbPath := fs.String("db", denieddb.DefaultPath(), "denial database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
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
	if len(pending) == 0 {
		fmt.Println("no denied hosts awaiting review")
		return nil
	}
	cfg, err := config.Load(absConfig)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, h := range cfg.Network.Allow {
		existing[h] = true
	}
	var hosts []string
	for _, r := range pending {
		hosts = append(hosts, r.Host)
	}
	added, err := config.AddAllowedHosts(absConfig, hosts)
	if err != nil {
		return err
	}
	addedSet := map[string]bool{}
	for _, h := range added {
		addedSet[h] = true
		fmt.Printf("added %s\n", h)
	}
	already := 0
	for _, h := range hosts {
		if !addedSet[h] {
			already++
			fmt.Printf("already allowed %s\n", h)
		}
	}
	if err := denieddb.MarkAllowed(db, hosts); err != nil {
		return fmt.Errorf("marking hosts allowed: %w", err)
	}
	fmt.Printf("%d host(s) added, %d already allowed\n", len(added), already)
	return nil
}
