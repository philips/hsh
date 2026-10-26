package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

func proxyAutoAllowCommand() *ff.Command {
	var dbPath, session string
	return newCommand("proxy-auto-allow", "hsh proxy-auto-allow --session ID", "allow a session's denied hosts in its config (TOFU)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", denieddb.DefaultPath(), "denial database path")
		fs.StringVar(&session, 0, "session", "", "the session whose denied hosts to allow")
	}, func(ctx context.Context, args []string) error {
		if session == "" {
			return fmt.Errorf("proxy-auto-allow requires --session: which session's config should the hosts be allowed in?")
		}
		sessionConfig := filepath.Join(denieddb.SessionDir(session), "config.json")
		if _, err := os.Stat(sessionConfig); err != nil {
			return fmt.Errorf("session %s has no config copy at %s (remove orphaned session state with: hsh gc)", session, sessionConfig)
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
		pending, err := denieddb.Pending(db, denieddb.Filter{Session: session})
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Println("no denied hosts awaiting review")
			return nil
		}
		cfg, err := config.Load(sessionConfig)
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
		added, err := config.AddAllowedHosts(sessionConfig, hosts)
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
		if err := denieddb.MarkAllowed(db, session, hosts); err != nil {
			return fmt.Errorf("marking hosts allowed: %w", err)
		}
		fmt.Printf("%d host(s) added, %d already allowed (session %s config: %s)\n", len(added), already, session, sessionConfig)
		return nil
	})
}
