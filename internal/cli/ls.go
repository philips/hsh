package cli

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/dustin/go-humanize"
)

// Ls implements `hsh ls`: list hsh sandboxes, following `podman pod ls`.
func Ls(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	var quiet bool
	fs.BoolVar(&quiet, "quiet", false, "print pod IDs only")
	fs.BoolVar(&quiet, "q", false, "print pod IDs only")
	var noHeading bool
	fs.BoolVar(&noHeading, "noheading", false, "do not print headers")
	fs.BoolVar(&noHeading, "n", false, "do not print headers")
	noTrunc := fs.Bool("no-trunc", false, "do not truncate pod and container IDs")
	sortBy := fs.String("sort", "created", "sort output by created, id, name, or number")
	filtersLong := fs.StringArray("filter", nil, "filter output (podman filter, repeatable)")
	filtersShort := fs.StringArray("f", nil, "filter output (podman filter, repeatable)")
	format := fs.String("format", "", "Go template to format the output (passed through to podman)")
	ctrIDs := fs.Bool("ctr-ids", false, "display container IDs (passed through to podman)")
	ctrNames := fs.Bool("ctr-names", false, "display container names (passed through to podman)")
	ctrStatus := fs.Bool("ctr-status", false, "display container status (passed through to podman)")
	ns := fs.Bool("ns", false, "display namespace information (passed through to podman)")
	var latest bool
	fs.BoolVar(&latest, "latest", false, "show only the most recently created sandbox")
	fs.BoolVar(&latest, "l", false, "show only the most recently created sandbox")
	workspace := fs.String("workspace", "", "show only the sandbox for this workspace directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *sortBy {
	case "created", "id", "name", "number":
	default:
		return fmt.Errorf("invalid --sort value %q (want created, id, name, or number)", *sortBy)
	}

	var extraFilters []string
	extraFilters = append(extraFilters, *filtersLong...)
	extraFilters = append(extraFilters, *filtersShort...)
	if *workspace != "" {
		extraFilters = append(extraFilters, "label=hsh.workspace="+*workspace)
	}

	// Pass-through mode: the user asked for podman's own rendering
	// (--format, --ctr-*, --ns), so hand the flags to podman with the hsh
	// selection added.
	if *format != "" || *ctrIDs || *ctrNames || *ctrStatus || *ns {
		podmanArgs := []string{"pod", "ls", "--filter", "name=hsh-"}
		for _, f := range extraFilters {
			podmanArgs = append(podmanArgs, "--filter", f)
		}
		if *format != "" {
			podmanArgs = append(podmanArgs, "--format", *format)
		}
		if *ctrIDs {
			podmanArgs = append(podmanArgs, "--ctr-ids")
		}
		if *ctrNames {
			podmanArgs = append(podmanArgs, "--ctr-names")
		}
		if *ctrStatus {
			podmanArgs = append(podmanArgs, "--ctr-status")
		}
		if *ns {
			podmanArgs = append(podmanArgs, "--ns")
		}
		if quiet {
			podmanArgs = append(podmanArgs, "--quiet")
		}
		if noHeading {
			podmanArgs = append(podmanArgs, "--noheading")
		}
		if *noTrunc {
			podmanArgs = append(podmanArgs, "--no-trunc")
		}
		podmanArgs = append(podmanArgs, "--sort", *sortBy)
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
	if latest && len(pods) > 0 {
		best := pods[0]
		for _, p := range pods[1:] {
			if p.CreatedTime().After(best.CreatedTime()) {
				best = p
			}
		}
		pods = []podInfo{best}
	}
	switch *sortBy {
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
		fmt.Fprintln(w, "POD ID\tNAME\tSTATUS\tCREATED\tINFRA ID\t# OF CONTAINERS\tWORKSPACE")
	}
	for _, p := range pods {
		id := p.ID
		if !*noTrunc && len(id) > 12 {
			id = id[:12]
		}
		infra := p.InfraID
		if !*noTrunc && len(infra) > 12 {
			infra = infra[:12]
		}
		ws := p.Workspace()
		if ws == "" {
			ws = "-"
		}
		created := p.Created
		if t := p.CreatedTime(); !t.IsZero() {
			created = humanize.Time(t)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", id, p.Name, p.Status, created, infra, p.NumContainers(), ws)
	}
	return w.Flush()
}
