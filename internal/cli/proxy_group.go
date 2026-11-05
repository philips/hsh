package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/state"
)

const watchInterval = 2 * time.Second

func proxyCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "proxy",
		Usage:     "hsh proxy SUBCOMMAND",
		ShortHelp: "review the egress proxy's denial records and decide hosts",
		Flags:     ff.NewFlagSet("proxy"),
		Subcommands: []*ff.Command{
			proxyDeniedCommand(),
			proxyAllowCommand(),
			proxyDenyCommand(),
			proxyRulesCommand(),
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func hostURL(h string) string {
	if strings.Contains(h, "*") {
		return h
	}
	return "https://" + h
}

func sessionConfigPath(session string) (string, error) {
	p := filepath.Join(state.SessionDir(session), "config.json")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("session %s has no config copy at %s (remove orphaned session state with: hsh rm --garbage)", session, p)
	}
	return p, nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func applyAllow(db *monitordb.Store, session string, hosts []string) (added, already []string, stillDenied map[string]string, err error) {
	unique := uniqueSorted(hosts)
	rules, err := db.Rules(session)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, h := range unique {
		if monitordb.Evaluate(rules, h).Access == monitordb.AccessAllow {
			already = append(already, h)
		} else {
			added = append(added, h)
		}
	}
	if err := db.Allow(session, added); err != nil {
		return nil, nil, nil, err
	}
	if rules, err = db.Rules(session); err != nil {
		return nil, nil, nil, err
	}
	stillDenied = map[string]string{}
	for _, h := range added {
		if m := monitordb.Evaluate(rules, h); m.Access == monitordb.AccessDeny {
			stillDenied[h] = m.Rule.DeniedBy()
		}
	}
	return added, already, stillDenied, nil
}

func allowHosts(db *monitordb.Store, session string, hosts []string) error {
	added, already, stillDenied, err := applyAllow(db, session, hosts)
	for _, h := range already {
		fmt.Printf("already allowed %s\n", hostURL(h))
	}
	for _, h := range added {
		if by, ok := stillDenied[h]; ok {
			fmt.Printf("added %s, but it stays %s\n", hostURL(h), by)
			continue
		}
		fmt.Printf("added %s\n", hostURL(h))
	}
	return err
}

func proxyDeniedCommand() *ff.Command {
	var watch, all bool
	var filter monitordb.Filter
	return newCommand("denied", "hsh proxy denied [FLAGS]", "list hosts denied by the egress proxy", func(fs *ff.FlagSet) {
		fs.BoolVar(&watch, 0, "watch", "continuously output new denied requests")
		fs.BoolVar(&all, 0, "all", "also list hosts already decided or quieted, with their state")
		sessionFilterFlags(fs, &filter)
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("proxy denied takes no positional arguments")
		}
		if all && watch {
			return errors.New("proxy denied takes either --all or --watch, not both")
		}
		if err := resolveFilterSession(&filter); err != nil {
			return err
		}
		db := monitordb.DefaultStore()
		if all {
			return printAllDenied(db, filter)
		}
		printRows := func(rows []monitordb.DeniedHost) {
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
		rows, err := db.Pending(filter)
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
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				rows, err := db.Pending(filter)
				if err != nil {
					return err
				}
				var fresh []monitordb.DeniedHost
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

func stateLabel(r monitordb.DeniedHost) string {
	if r.Denied() {
		return r.DeniedBy
	}
	return r.State
}

func printAllDenied(db *monitordb.Store, filter monitordb.Filter) error {
	rows, err := db.All(filter)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no denied hosts recorded")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if filter.Session != "" || filter.Workspace != "" {
		fmt.Fprintln(w, "URL\tSTATE\tCOUNT\tLAST SEEN")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", hostURL(r.Host), stateLabel(r), r.Count, r.LastSeen)
		}
		return w.Flush()
	}
	fmt.Fprintln(w, "SESSION ID\tWORKSPACE\tURL\tSTATE\tCOUNT\tLAST SEEN")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", r.Session, r.Workspace, hostURL(r.Host), stateLabel(r), r.Count, r.LastSeen)
	}
	return w.Flush()
}

func proxyDenyCommand() *ff.Command {
	var session string
	return newCommand("deny", "hsh proxy deny [FLAGS] URL|PATTERN", "deny a host (or *.domain) for a session, in every network mode", func(fs *ff.FlagSet) {
		fs.StringVar(&session, 0, "session", "", "the session the host is denied in")
	}, func(ctx context.Context, args []string) error {
		if session == "" {
			return errors.New("proxy deny requires --session: which session should the host be denied in?")
		}
		if len(args) != 1 {
			return errors.New("proxy deny takes a single URL, host, or *.domain")
		}
		session, err := resolveSessionRef(session)
		if err != nil {
			return err
		}
		if _, err := sessionConfigPath(session); err != nil {
			return err
		}
		pattern, err := config.HostPattern(args[0])
		if err != nil {
			return err
		}
		if err := monitordb.DefaultStore().Deny(session, []string{pattern}); err != nil {
			return err
		}
		fmt.Printf("denied %s\n", pattern)
		return nil
	})
}

func proxyAllowCommand() *ff.Command {
	var session string
	var all, watch bool
	return newCommand("allow", "hsh proxy allow [FLAGS] [--all | URL|PATTERN]", "allow a session's denied hosts (TOFU)", func(fs *ff.FlagSet) {
		fs.StringVar(&session, 0, "session", "", "the session the hosts are allowed in")
		fs.BoolVar(&all, 0, "all", "allow every host currently denied for the session")
		fs.BoolVar(&watch, 0, "watch", "continuously approve new denied requests")
	}, func(ctx context.Context, args []string) error {
		if session == "" {
			return errors.New("proxy allow requires --session: which session should the hosts be allowed in?")
		}
		session, err := resolveSessionRef(session)
		if err != nil {
			return err
		}
		if all && len(args) > 0 {
			return errors.New("proxy allow takes either --all or a single URL, not both")
		}
		if !all && len(args) != 1 {
			return errors.New("proxy allow takes a single URL (or --all)")
		}
		if _, err := sessionConfigPath(session); err != nil {
			return err
		}
		db := monitordb.DefaultStore()
		if watch {
			defer watchSessions(monitordb.Filter{Session: session})()
		}
		if !all {
			host, err := config.HostPattern(args[0])
			if err != nil {
				return err
			}
			if err := allowHosts(db, session, []string{host}); err != nil {
				return err
			}
			if !watch {
				return nil
			}
		}
		var only string
		if !all {
			only, _ = config.HostPattern(args[0])
		}
		approved := map[string]bool{}
		approve := func() error {
			pending, err := db.Pending(monitordb.Filter{Session: session})
			if err != nil {
				return err
			}
			var fresh []string
			for _, r := range pending {
				if only != "" && !config.HostMatches([]string{only}, r.Host) {
					continue
				}
				if !approved[r.Host] {
					approved[r.Host] = true
					fresh = append(fresh, r.Host)
				}
			}
			if len(fresh) > 0 {
				return allowHosts(db, session, fresh)
			}
			return nil
		}
		if err := approve(); err != nil {
			return err
		}
		if !watch {
			return nil
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
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
