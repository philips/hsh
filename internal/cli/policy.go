package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/monitordb"
	"hsh/internal/policy"
)

func policyCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "policy",
		Usage:     "hsh policy SUBCOMMAND",
		ShortHelp: "manage network policy files and apply them to sessions",
		LongHelp: "Network policies are JSON files in " + policy.Dir() + ", one per name.\n" +
			"Applying a policy copies its rules into a session; editing or removing the\n" +
			"file later does not change sessions that applied it.",
		Flags: ff.NewFlagSet("policy"),
		Subcommands: []*ff.Command{
			policyLsCommand(),
			policyShowCommand(),
			policyAddCommand(),
			policyRmCommand(),
			policyApplyCommand(),
			policyUnapplyCommand(),
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func oneName(args []string, usage string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: %s", usage)
	}
	return args[0], policy.ValidName(args[0])
}

func policySession(explicit, retry string) (string, error) {
	if explicit == "" {
		id, err := resolveSession("", retry)
		if err != nil {
			return "", fmt.Errorf("%w (or pass --session)", err)
		}
		return id, nil
	}
	id, err := resolveSessionRef(explicit)
	if err != nil {
		return "", err
	}
	if _, err := sessionConfigPath(id); err != nil {
		return "", err
	}
	return id, nil
}

func describeRule(r policy.Rule) string {
	var parts []string
	if r.Access != "" {
		parts = append(parts, r.Access)
	}
	if r.Log != "" {
		parts = append(parts, "log "+r.Log)
	}
	return fmt.Sprintf("%-18s %s", strings.Join(parts, ", "), strings.Join(r.Hosts, ", "))
}

func policyLsCommand() *ff.Command {
	return newCommand("ls", "hsh policy ls", "list network policies", nil, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return errors.New("policy ls takes no arguments")
		}
		entries, err := policy.List()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Printf("no policies in %s (add one with: hsh policy add NAME FILE)\n", policy.Dir())
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tRULES\tDESCRIPTION")
		for _, e := range entries {
			if e.Err != nil {
				fmt.Fprintf(w, "%s\t-\tinvalid: %v\n", e.Name, e.Err)
				continue
			}
			fmt.Fprintf(w, "%s\t%d\t%s\n", e.Name, len(e.Policy.Rules), e.Policy.Description)
		}
		return w.Flush()
	})
}

func policyShowCommand() *ff.Command {
	return newCommand("show", "hsh policy show NAME", "print a network policy", nil, func(ctx context.Context, args []string) error {
		name, err := oneName(args, "hsh policy show NAME")
		if err != nil {
			return err
		}
		p, err := policy.Load(name)
		if err != nil {
			return err
		}
		data, err := p.Marshal()
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	})
}

func policyAddCommand() *ff.Command {
	var force bool
	return newCommand("add", "hsh policy add [--force] NAME FILE|-", "validate a network policy file and store it under NAME", func(fs *ff.FlagSet) {
		fs.BoolVar(&force, 0, "force", "replace an existing policy of the same name")
	}, func(ctx context.Context, args []string) error {
		if len(args) != 2 {
			return errors.New("usage: hsh policy add [--force] NAME FILE|-")
		}
		name, src := args[0], args[1]
		if err := policy.ValidName(name); err != nil {
			return err
		}
		var data []byte
		var err error
		if src == "-" {
			data, err = io.ReadAll(os.Stdin)
			src = "stdin"
		} else {
			data, err = os.ReadFile(src)
		}
		if err != nil {
			return err
		}
		p, err := policy.Parse(data, src)
		if err != nil {
			return err
		}
		if err := policy.Save(name, p, force); err != nil {
			return err
		}
		fmt.Printf("added policy %s (%s):\n", name, policy.Path(name))
		for _, r := range p.Rules {
			fmt.Printf("  %s\n", describeRule(r))
		}
		return nil
	})
}

func policyRmCommand() *ff.Command {
	return newCommand("rm", "hsh policy rm NAME", "remove a network policy (sessions that applied it keep their copy)", nil, func(ctx context.Context, args []string) error {
		name, err := oneName(args, "hsh policy rm NAME")
		if err != nil {
			return err
		}
		if err := policy.Remove(name); err != nil {
			return err
		}
		fmt.Printf("removed policy %s; sessions that applied it keep their copy (hsh policy unapply %s removes it from a session)\n", name, name)
		return nil
	})
}

func policyApplyCommand() *ff.Command {
	var session string
	return newCommand("apply", "hsh policy apply [--session ID] NAME", "copy a network policy's rules into a session", func(fs *ff.FlagSet) {
		fs.StringVar(&session, 0, "session", "", "the session (default: this workspace's attached or only session)")
	}, func(ctx context.Context, args []string) error {
		name, err := oneName(args, "hsh policy apply [--session ID] NAME")
		if err != nil {
			return err
		}
		p, err := policy.Load(name)
		if err != nil {
			return err
		}
		id, err := policySession(session, "hsh policy apply --session ID "+name)
		if err != nil {
			return err
		}
		rules := p.SessionRules(name)
		if err := monitordb.DefaultStore().ApplyPolicy(id, name, rules); err != nil {
			return err
		}
		fmt.Printf("applied policy %s to session %s: %d host rule(s); the proxy picks them up within a second\n", name, id, len(rules))
		return nil
	})
}

func policyUnapplyCommand() *ff.Command {
	var session string
	return newCommand("unapply", "hsh policy unapply [--session ID] NAME", "remove a policy's rules from a session", func(fs *ff.FlagSet) {
		fs.StringVar(&session, 0, "session", "", "the session (default: this workspace's attached or only session)")
	}, func(ctx context.Context, args []string) error {
		name, err := oneName(args, "hsh policy unapply [--session ID] NAME")
		if err != nil {
			return err
		}
		id, err := policySession(session, "hsh policy unapply --session ID "+name)
		if err != nil {
			return err
		}
		n, err := monitordb.DefaultStore().UnapplyPolicy(id, name)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("policy %s is not applied to session %s", name, id)
		}
		fmt.Printf("removed %d rule(s) of policy %s from session %s\n", n, name, id)
		return nil
	})
}

func proxyRulesCommand() *ff.Command {
	var session string
	return newCommand("rules", "hsh proxy rules [--session ID]", "list a session's allow, deny, and log rules with their source", func(fs *ff.FlagSet) {
		fs.StringVar(&session, 0, "session", "", "the session (default: this workspace's attached or only session)")
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return errors.New("proxy rules takes no positional arguments")
		}
		id, err := policySession(session, "hsh proxy rules --session ID")
		if err != nil {
			return err
		}
		rules, err := monitordb.DefaultStore().Rules(id)
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			fmt.Printf("session %s has no rules\n", id)
			return nil
		}
		dash := func(s string) string {
			if s == "" {
				return "-"
			}
			return s
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PATTERN\tACCESS\tLOG\tSOURCE\tDECIDED")
		for _, r := range rules {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Pattern, dash(r.Access), dash(r.Log), r.Source, r.DecidedAt)
		}
		return w.Flush()
	})
}
