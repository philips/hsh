package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/denieddb"
)

func proxyDeniedCommand() *ff.Command {
	var dbPath string
	var all, dns bool
	var filter denieddb.Filter
	return newCommand("proxy-denied", "hsh proxy-denied [FLAGS]", "list hosts denied by the egress proxy", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", denieddb.DefaultPath(), "denial database path")
		fs.BoolVar(&all, 0, "all", "include hosts that were already allowed")
		fs.BoolVar(&dns, 0, "dns", "list DNS query denials instead of HTTP egress denials")
		sessionFilterFlags(fs, &filter)
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("proxy-denied takes no positional arguments")
		}
		db, err := denieddb.OpenReadOnly(dbPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Println("no denied hosts recorded")
				return nil
			}
			return err
		}
		defer db.Close()
		var rows []denieddb.DeniedHost
		switch {
		case dns && all:
			rows, err = denieddb.AllDNS(db, filter)
		case dns:
			rows, err = denieddb.PendingDNS(db, filter)
		case all:
			rows, err = denieddb.All(db, filter)
		default:
			rows, err = denieddb.Pending(db, filter)
		}
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("no denied hosts awaiting review")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "HOST\tSESSION\tWORKSPACE\tTARGET\tCOUNT\tFIRST SEEN\tLAST SEEN")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.Host, r.Session, r.Workspace, r.Target, r.Count, r.FirstSeen, r.LastSeen)
		}
		return w.Flush()
	})
}
