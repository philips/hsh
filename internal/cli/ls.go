package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
	"github.com/peterbourgon/ff/v4"
)

func lsCommand() *ff.Command {
	var quiet, noHeading, noTrunc, ctrIDs, ctrNames, ctrStatus, ns, latest bool
	var sortBy, format, workspace string
	var filters []string
	return newCommand("ls", "hsh ls [FLAGS]", "list hsh sandboxes (pods), like podman pod ls", func(fs *ff.FlagSet) {
		fs.BoolVar(&quiet, 'q', "quiet", "print pod IDs only")
		fs.BoolVar(&noHeading, 'n', "noheading", "do not print headers")
		fs.BoolVar(&noTrunc, 0, "no-trunc", "do not truncate pod and container IDs")
		fs.StringVar(&sortBy, 0, "sort", "created", "sort output by created, id, name, or number")
		fs.StringListVar(&filters, 'f', "filter", "filter output (podman filter, repeatable)")
		fs.StringVar(&format, 0, "format", "", "Go template to format the output (passed through to podman)")
		fs.BoolVar(&ctrIDs, 0, "ctr-ids", "display container IDs (passed through to podman)")
		fs.BoolVar(&ctrNames, 0, "ctr-names", "display container names (passed through to podman)")
		fs.BoolVar(&ctrStatus, 0, "ctr-status", "display container status (passed through to podman)")
		fs.BoolVar(&ns, 0, "ns", "display namespace information (passed through to podman)")
		fs.BoolVar(&latest, 'l', "latest", "show only the most recently created sandbox")
		fs.StringVar(&workspace, 0, "workspace", "", "show only the sandbox for this workspace directory")
	}, func(ctx context.Context, args []string) error {
		switch sortBy {
		case "created", "id", "name", "number":
		default:
			return fmt.Errorf("invalid --sort value %q (want created, id, name, or number)", sortBy)
		}

		var extraFilters []string
		extraFilters = append(extraFilters, filters...)
		if workspace != "" {
			extraFilters = append(extraFilters, "label=hsh.workspace="+workspace)
		}

		if format != "" || ctrIDs || ctrNames || ctrStatus || ns {
			podmanArgs := []string{"pod", "ls", "--filter", "name=hsh-"}
			for _, f := range extraFilters {
				podmanArgs = append(podmanArgs, "--filter", f)
			}
			if format != "" {
				podmanArgs = append(podmanArgs, "--format", format)
			}
			if ctrIDs {
				podmanArgs = append(podmanArgs, "--ctr-ids")
			}
			if ctrNames {
				podmanArgs = append(podmanArgs, "--ctr-names")
			}
			if ctrStatus {
				podmanArgs = append(podmanArgs, "--ctr-status")
			}
			if ns {
				podmanArgs = append(podmanArgs, "--ns")
			}
			if quiet {
				podmanArgs = append(podmanArgs, "--quiet")
			}
			if noHeading {
				podmanArgs = append(podmanArgs, "--noheading")
			}
			if noTrunc {
				podmanArgs = append(podmanArgs, "--no-trunc")
			}
			podmanArgs = append(podmanArgs, "--sort", sortBy)
			if latest {
				podmanArgs = append(podmanArgs, "--latest")
			}
			out, err := podman(podmanArgs...)
			if err != nil {
				return fmt.Errorf("podman pod ls: %w\n%s", err, out)
			}
			fmt.Print(out)
			return nil
		}

		pods, err := listSandboxes(extraFilters...)
		if err != nil {
			return err
		}
		pods = sandboxPods(pods)
		if latest {
			if p, ok := latestPod(pods); ok {
				pods = []podInfo{p}
			} else {
				pods = nil
			}
		}
		switch sortBy {
		case "created":
			sort.Slice(pods, func(i, j int) bool { return pods[i].CreatedTime().After(pods[j].CreatedTime()) })
		case "id":
			sort.Slice(pods, func(i, j int) bool { return pods[i].ID < pods[j].ID })
		case "name":
			sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		case "number":
			sort.Slice(pods, func(i, j int) bool { return pods[i].NumContainers() < pods[j].NumContainers() })
		}

		if quiet {
			for _, p := range pods {
				fmt.Println(p.ID)
			}
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if !noHeading {
			fmt.Fprintln(w, "SESSION\tSTATUS\tCREATED\t# OF CONTAINERS\tWORKSPACE")
		}
		for _, p := range pods {
			ws := p.Workspace()
			if ws == "" {
				ws = "-"
			}
			created := p.Created
			if t := p.CreatedTime(); !t.IsZero() {
				created = humanize.Time(t)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", p.Session(), p.Status, created, p.NumContainers(), ws)
		}
		return w.Flush()
	})
}
