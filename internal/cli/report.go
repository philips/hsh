package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/policy"
)

const reportPoll = 250 * time.Millisecond

func proxyReportCommand() *ff.Command {
	var session, format string
	var wait, denied bool
	var since int64
	var timeout time.Duration
	return newCommand("report", "hsh proxy report [--session ID] [--format text|policy]\n  hsh proxy report --wait [--session ID] [--since ID] [--denied] [--timeout DUR] [--format text|json]",
		"summarize a session's connection log, or wait for its next entries", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "the session (default: this workspace's attached or only session)")
			fs.StringVar(&format, 0, "format", "text", "text, policy (a draft policy of the allowed hosts), or json (with --wait)")
			fs.BoolVar(&wait, 0, "wait", "block until the log has new entries, print them, and exit")
			fs.Int64Var(&since, 0, "since", -1, "with --wait: entries after this id; -1 means entries added after the command starts")
			fs.BoolVar(&denied, 0, "denied", "with --wait: only denied entries")
			fs.DurationVar(&timeout, 0, "timeout", 0, "with --wait: give up after this long with exit status 2; 0 waits until interrupted")
		}, func(ctx context.Context, args []string) error {
			if len(args) > 0 {
				return errors.New("proxy report takes no positional arguments")
			}
			if !wait && (since >= 0 || denied || timeout != 0) {
				return errors.New("--since, --denied, and --timeout need --wait")
			}
			switch {
			case wait && format != "text" && format != "json":
				return fmt.Errorf("invalid --format %q with --wait (want text or json)", format)
			case !wait && format != "text" && format != "policy":
				return fmt.Errorf("invalid --format %q (want text or policy)", format)
			}
			id, err := policySession(session, "hsh proxy report --session ID")
			if err != nil {
				return err
			}
			db := monitordb.DefaultStore()
			if wait {
				return waitForConnections(ctx, db, id, since, denied, timeout, format)
			}
			rows, err := db.Connections(id, 0, monitordb.MaxConnections)
			if err != nil {
				return err
			}
			if format == "policy" {
				return printDraftPolicy(id, rows)
			}
			printReport(id, rows)
			return nil
		})
}

type hostSummary struct {
	host     string
	count    int
	ports    map[int]bool
	schemes  map[string]bool
	up, down int64
	last     string
	allowed  bool
	denied   bool
	held     int
}

func printReport(session string, rows []monitordb.Connection) {
	if len(rows) == 0 {
		fmt.Printf("session %s has no connections logged\n", session)
		return
	}
	by := map[string]*hostSummary{}
	for _, r := range rows {
		h := by[r.Host]
		if h == nil {
			h = &hostSummary{host: r.Host, ports: map[int]bool{}, schemes: map[string]bool{}}
			by[r.Host] = h
		}
		h.count++
		h.ports[r.Port] = true
		h.schemes[r.Scheme] = true
		h.up += r.BytesUp
		h.down += r.BytesDown
		if r.Time > h.last {
			h.last = r.Time
		}
		if r.Access == monitordb.AccessAllow {
			h.allowed = true
		} else {
			h.denied = true
		}
	}
	list := make([]*hostSummary, 0, len(by))
	for _, h := range by {
		list = append(list, h)
	}
	sort.Slice(list, func(a, b int) bool {
		if list[a].count != list[b].count {
			return list[a].count > list[b].count
		}
		return list[a].host < list[b].host
	})
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tCONNECTIONS\tPORTS\tSCHEMES\tUP\tDOWN\tLAST SEEN\tACCESS")
	for _, h := range list {
		var ports []int
		for p := range h.ports {
			ports = append(ports, p)
		}
		sort.Ints(ports)
		ps := make([]string, len(ports))
		for i, p := range ports {
			ps[i] = strconv.Itoa(p)
		}
		var schemes []string
		for s := range h.schemes {
			schemes = append(schemes, s)
		}
		sort.Strings(schemes)
		access := "allowed"
		switch {
		case h.allowed && h.denied:
			access = "allowed, denied"
		case h.denied:
			access = "denied"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%d\t%s\t%s\n", h.host, h.count, strings.Join(ps, ","), strings.Join(schemes, ","), h.up, h.down, h.last, access)
	}
	w.Flush()
}

func printDraftPolicy(session string, rows []monitordb.Connection) error {
	seen := map[string]bool{}
	var hosts []string
	for _, r := range rows {
		if r.Access != monitordb.AccessAllow {
			continue
		}
		h, err := config.CanonicalPattern(r.Host)
		if err != nil || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		return fmt.Errorf("session %s has no allowed connections logged", session)
	}
	sort.Strings(hosts)
	p := policy.Policy{
		Version:     policy.Version,
		Description: "draft from the connection log of session " + session + "; review before hsh policy add",
		Rules:       []policy.Rule{{Hosts: hosts, Access: monitordb.AccessAllow}},
	}
	data, err := p.Marshal()
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printConnection(c monitordb.Connection, format string) error {
	if format == "json" {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("%d %s %s %d %s %s %s %s %s\n", c.ID, c.Time, c.Host, c.Port, c.Scheme, c.Access, c.Log, dash(c.Pattern), dash(c.Source))
	return nil
}

func waitForConnections(ctx context.Context, db *monitordb.Store, session string, since int64, deniedOnly bool, timeout time.Duration, format string) error {
	if since < 0 {
		last, err := db.LastConnectionID(session)
		if err != nil {
			return err
		}
		since = last
	}
	defer watchSessions(monitordb.Filter{Session: session})()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	tick := time.NewTicker(reportPoll)
	defer tick.Stop()
	var mtime time.Time
	var size int64
	first := true
	for {
		var changed bool
		mtime, size, changed = db.ConnLogChanged(session, mtime, size)
		if changed || first {
			first = false
			rows, err := db.Connections(session, since, 1000)
			if err != nil {
				return err
			}
			var out []monitordb.Connection
			for _, r := range rows {
				since = r.ID
				if deniedOnly && r.Access != monitordb.AccessDeny {
					continue
				}
				out = append(out, r)
			}
			if len(out) > 0 {
				for _, c := range out {
					if err := printConnection(c, format); err != nil {
						return err
					}
				}
				return nil
			}
			if len(rows) == 1000 {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			return &exitStatusError{code: 2, err: fmt.Errorf("no new connection log entries for session %s within %s", session, timeout)}
		case <-tick.C:
		}
	}
}
