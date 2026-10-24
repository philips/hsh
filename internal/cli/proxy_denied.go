package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"harnessh/internal/denieddb"
)

func ProxyDenied(args []string) error {
	fs := flag.NewFlagSet("proxy-denied", flag.ContinueOnError)
	dbPath := fs.String("db", denieddb.DefaultPath(), "denial database path")
	all := fs.Bool("all", false, "include hosts that were already allowed")
	dnsFlag := fs.Bool("dns", false, "list DNS query denials instead of HTTP egress denials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := denieddb.OpenReadOnly(*dbPath)
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
	case *dnsFlag && *all:
		rows, err = denieddb.AllDNS(db)
	case *dnsFlag:
		rows, err = denieddb.PendingDNS(db)
	case *all:
		rows, err = denieddb.All(db)
	default:
		rows, err = denieddb.Pending(db)
	}
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no denied hosts awaiting review")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tTARGET\tCOUNT\tFIRST SEEN\tLAST SEEN")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", r.Host, r.Target, r.Count, r.FirstSeen, r.LastSeen)
	}
	return w.Flush()
}
