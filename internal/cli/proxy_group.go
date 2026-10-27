package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

const watchInterval = 2 * time.Second

func proxyCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "proxy",
		Usage:     "hsh proxy SUBCOMMAND",
		ShortHelp: "review and manage the egress proxy's denial records",
		Flags:     ff.NewFlagSet("proxy"),
		Subcommands: []*ff.Command{
			proxyDeniedCommand(),
			proxyAllowCommand(),
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func hostURL(h string) string {
	return "https://" + h
}

func hostFromRef(ref string) string {
	s := ref
	if i := strings.Index(s, "://"); i >= 0 {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s, "]") {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			s = s[:i]
		}
	}
	return s
}

func sessionConfigPath(session string) (string, error) {
	p := filepath.Join(denieddb.SessionDir(session), "config.json")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("session %s has no config copy at %s (remove orphaned session state with: hsh gc)", session, p)
	}
	return p, nil
}

func allowHosts(db *sql.DB, session, cfgPath string, hosts []string) error {
	existing := map[string]bool{}
	addedSet := map[string]bool{}
	var added []string
	for _, h := range hosts {
		if !addedSet[h] {
			addedSet[h] = true
			added = append(added, h)
		}
	}
	sort.Strings(added)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for _, h := range cfg.Network.Allow {
		existing[h] = true
	}
	var toAdd []string
	for _, h := range added {
		if !existing[h] {
			toAdd = append(toAdd, h)
		}
	}
	if len(toAdd) > 0 {
		if _, err := config.AddAllowedHosts(cfgPath, toAdd); err != nil {
			return err
		}
	}
	for _, h := range added {
		if existing[h] {
			fmt.Printf("already allowed %s\n", hostURL(h))
		} else {
			fmt.Printf("added %s\n", hostURL(h))
		}
	}
	return denieddb.MarkAllowed(db, session, added)
}

func proxyDeniedCommand() *ff.Command {
	var dbPath string
	var watch bool
	var filter denieddb.Filter
	return newCommand("denied", "hsh proxy denied [FLAGS]", "list hosts denied by the egress proxy (DNS lookups share this queue)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", denieddb.DefaultPath(), "denial database path")
		fs.BoolVar(&watch, 0, "watch", "continuously output new denied requests")
		sessionFilterFlags(fs, &filter)
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("proxy denied takes no positional arguments")
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
		printRows := func(rows []denieddb.DeniedHost) {
			if filter.Session != "" || filter.Workspace != "" {
				for _, r := range rows {
					fmt.Println(hostURL(r.Host))
				}
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION ID\tWORKSPACE\tURL\tCOUNT\tLAST SEEN")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.Session, r.Workspace, hostURL(r.Host), r.Count, r.LastSeen)
			}
			w.Flush()
		}
		rows, err := denieddb.Pending(db, filter)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("no denied hosts awaiting review")
		} else {
			printRows(rows)
		}
		if !watch {
			return nil
		}
		seen := map[string]bool{}
		for _, r := range rows {
			seen[r.Host+"@"+r.Session] = true
		}
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				rows, err := denieddb.Pending(db, filter)
				if err != nil {
					return err
				}
				var fresh []denieddb.DeniedHost
				for _, r := range rows {
					k := r.Host + "@" + r.Session
					if !seen[k] {
						seen[k] = true
						fresh = append(fresh, r)
					}
				}
				if len(fresh) > 0 {
					printRows(fresh)
				}
			}
		}
	})
}

func proxyAllowCommand() *ff.Command {
	var dbPath, session string
	var all, watch bool
	return newCommand("allow", "hsh proxy allow [FLAGS] [--all | URL]", "allow a session's denied hosts in its config (TOFU)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", denieddb.DefaultPath(), "denial database path")
		fs.StringVar(&session, 0, "session", "", "the session whose config the hosts are allowed in")
		fs.BoolVar(&all, 0, "all", "allow every host currently denied for the session")
		fs.BoolVar(&watch, 0, "watch", "continuously approve new denied requests")
	}, func(ctx context.Context, args []string) error {
		if session == "" {
			return errors.New("proxy allow requires --session: which session's config should the hosts be allowed in?")
		}
		if all && len(args) > 0 {
			return errors.New("proxy allow takes either --all or a single URL, not both")
		}
		if !all && len(args) != 1 {
			return errors.New("proxy allow takes a single URL (or --all)")
		}
		cfgPath, err := sessionConfigPath(session)
		if err != nil {
			return err
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
		if !all {
			host := hostFromRef(args[0])
			if host == "" {
				return fmt.Errorf("could not extract a host from %q", args[0])
			}
			if err := allowHosts(db, session, cfgPath, []string{host}); err != nil {
				return err
			}
			if !watch {
				return nil
			}
		}
		var onlyHost string
		if !all {
			onlyHost = hostFromRef(args[0])
		}
		approved := map[string]bool{}
		approve := func() error {
			pending, err := denieddb.Pending(db, denieddb.Filter{Session: session})
			if err != nil {
				return err
			}
			var fresh []string
			for _, r := range pending {
				if onlyHost != "" && r.Host != onlyHost {
					continue
				}
				if !approved[r.Host] {
					approved[r.Host] = true
					fresh = append(fresh, r.Host)
				}
			}
			if len(fresh) > 0 {
				return allowHosts(db, session, cfgPath, fresh)
			}
			return nil
		}
		if err := approve(); err != nil {
			return err
		}
		if !watch {
			return nil
		}
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				if err := approve(); err != nil {
					return err
				}
			}
		}
	})
}
