package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"
	"golang.org/x/term"

	"hsh/internal/secretstore"
)

const secretUsage = `usage:
  hsh secret add NAME --host HOST [--host HOST]... [--db PATH] [--force] [--value-stdin]
  hsh secret allow NAME HOST... [--db PATH]
  hsh secret disallow NAME HOST... [--db PATH]
  hsh secret edit NAME [--db PATH]
  hsh secret rm NAME [--db PATH]
  hsh secret ls [--db PATH]

Secrets are stored unencrypted in the hsh secret store (default
$XDG_CONFIG_HOME/hsh/secrets.db, mode 0600), readable only by your user —
the same trust level as ~/.netrc or ~/.aws/credentials. Config files
reference them as "hsh://NAME". A secret is only ever sent to the hosts
you approved for it, whatever a config or image label asks for.
`

const firstUseWarning = `hsh: note: secrets are stored unencrypted in %s
(readable only by your user — the same trust level as ~/.netrc or ~/.aws/credentials)`

func secretCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "secret",
		Usage:     "hsh secret SUBCOMMAND",
		ShortHelp: "manage the hsh secret store and the hosts each secret may be sent to",
		Flags:     ff.NewFlagSet("secret"),
		Subcommands: []*ff.Command{
			secretAddCommand(),
			secretAllowCommand(),
			secretDisallowCommand(),
			secretEditCommand(),
			secretRmCommand(),
			secretLsCommand(),
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func openStore(dbPath string) (*secretstore.DB, error) {
	db, created, err := secretstore.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if created {
		fmt.Fprintf(os.Stderr, firstUseWarning+"\n", dbPath)
	}
	return db, nil
}

func secretAddCommand() *ff.Command {
	var dbPath string
	var force, valueStdin bool
	var hosts stringArray
	return newCommand("add", "hsh secret add [FLAGS] NAME", "store a secret and the hosts it may be sent to (hidden prompt, or --value-stdin)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", secretstore.DefaultPath(), "path to the secret store")
		fs.Value(0, "host", &hosts, "a host the secret may be sent to, such as api.example.com or *.example.com (repeatable, or comma-separated)")
		fs.BoolVar(&force, 0, "force", "overwrite an existing secret's value (keeps its hosts unless --host is given)")
		fs.BoolVar(&valueStdin, 0, "value-stdin", "read the value from stdin (for scripting)")
	}, func(ctx context.Context, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("secret add requires exactly one NAME\n\n%s", secretUsage)
		}
		name := args[0]
		if name == "" {
			return fmt.Errorf("secret name must not be empty")
		}
		hostList, err := secretstore.NormalizeHosts(splitHosts(hosts))
		if err != nil {
			return err
		}
		keepHosts := len(hostList) == 0
		if keepHosts && !staticSecretExists(dbPath, name) {
			return fmt.Errorf("secret add needs --host HOST: the hosts %s may be sent to (for example --host api.example.com)", name)
		}
		var value string
		if valueStdin {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read value from stdin: %w", err)
			}
			value = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		} else {
			if !isTerminal(os.Stdin) {
				return fmt.Errorf("stdin is not a terminal; use --value-stdin to pipe the value")
			}
			fmt.Fprintf(os.Stderr, "Enter value for %s: ", name)
			hidden, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return fmt.Errorf("read value: %w", err)
			}
			value = string(hidden)
		}
		if value == "" {
			return fmt.Errorf("secret value must not be empty")
		}
		db, err := openStore(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.Set(name, value, force); err != nil {
			return err
		}
		if !keepHosts {
			if err := db.SetHosts(name, hostList); err != nil {
				return err
			}
		}
		current, err := db.SecretHosts(name)
		if err != nil {
			return err
		}
		fmt.Printf("stored secret %s (sent only to %s)\n", name, strings.Join(current, ", "))
		return nil
	})
}

func splitHosts(in []string) []string {
	var out []string
	for _, v := range in {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				out = append(out, h)
			}
		}
	}
	return out
}

func staticSecretExists(dbPath, name string) bool {
	if !secretstore.Exists(dbPath) {
		return false
	}
	db, _, err := secretstore.Open(dbPath)
	if err != nil {
		return false
	}
	defer db.Close()
	_, err = db.SecretHosts(name)
	return err == nil
}

func openExistingStore(dbPath string) (*secretstore.DB, error) {
	if !secretstore.Exists(dbPath) {
		return nil, fmt.Errorf("no secret store at %s", dbPath)
	}
	db, _, err := secretstore.Open(dbPath)
	return db, err
}

func printSecretHosts(name string, hosts []string) {
	if len(hosts) == 0 {
		fmt.Printf("%s is not sent to any host\n", name)
		return
	}
	fmt.Printf("%s is sent only to: %s\n", name, strings.Join(hosts, ", "))
}

func secretHostsCommand(verb, help string, apply func(db *secretstore.DB, name string, hosts []string) ([]string, error)) *ff.Command {
	var dbPath string
	return newCommand(verb, "hsh secret "+verb+" [FLAGS] NAME HOST...", help, func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", secretstore.DefaultPath(), "path to the secret store")
	}, func(ctx context.Context, args []string) error {
		if len(args) < 2 {
			return fmt.Errorf("secret %s requires a NAME and at least one HOST\n\n%s", verb, secretUsage)
		}
		db, err := openExistingStore(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		hosts, err := apply(db, args[0], splitHosts(args[1:]))
		if err != nil {
			return err
		}
		printSecretHosts(args[0], hosts)
		return nil
	})
}

func secretAllowCommand() *ff.Command {
	return secretHostsCommand("allow", "let a secret be sent to more hosts", func(db *secretstore.DB, name string, hosts []string) ([]string, error) {
		return db.AllowHosts(name, hosts...)
	})
}

func secretDisallowCommand() *ff.Command {
	return secretHostsCommand("disallow", "stop sending a secret to some hosts", func(db *secretstore.DB, name string, hosts []string) ([]string, error) {
		return db.DisallowHosts(name, hosts...)
	})
}

const hostsFileHeader = `# Hosts hsh may send the secret %s to, one per line.
# A host is a name such as api.example.com, or *.example.com for its subdomains.
# Lines starting with # are ignored. Save an empty list to send it nowhere.
`

func parseHostsFile(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.Fields(line)...)
	}
	return out
}

func secretEditCommand() *ff.Command {
	var dbPath string
	return newCommand("edit", "hsh secret edit [FLAGS] NAME", "edit the hosts a secret may be sent to, in $EDITOR", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", secretstore.DefaultPath(), "path to the secret store")
	}, func(ctx context.Context, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("secret edit requires exactly one NAME\n\n%s", secretUsage)
		}
		name := args[0]
		db, err := openExistingStore(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		current, err := db.SecretHosts(name)
		if err != nil {
			return fmt.Errorf("secret %s: %w", name, err)
		}
		f, err := os.CreateTemp("", "hsh-secret-hosts-*.txt")
		if err != nil {
			return err
		}
		path := f.Name()
		defer os.Remove(path)
		content := fmt.Sprintf(hostsFileHeader, name) + strings.Join(current, "\n")
		if len(current) > 0 {
			content += "\n"
		}
		if _, err := f.WriteString(content); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		for {
			if err := runEditor(path); err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hosts, err := secretstore.NormalizeHosts(parseHostsFile(data))
			if err == nil {
				if err := db.SetHosts(name, hosts); err != nil {
					return err
				}
				printSecretHosts(name, hosts)
				return nil
			}
			if !isTerminal(os.Stdin) {
				return err
			}
			fmt.Fprintf(os.Stderr, "hsh: %v\nedit again? [Y/n] ", err)
			var answer string
			_, _ = fmt.Fscanln(os.Stdin, &answer)
			if a := strings.ToLower(strings.TrimSpace(answer)); a == "n" || a == "no" {
				return fmt.Errorf("hosts of %s unchanged", name)
			}
		}
	})
}

func secretRmCommand() *ff.Command {
	var dbPath string
	return newCommand("rm", "hsh secret rm [FLAGS] NAME", "remove a secret from the store", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", secretstore.DefaultPath(), "path to the secret store")
	}, func(ctx context.Context, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("secret rm requires exactly one NAME\n\n%s", secretUsage)
		}
		name := args[0]
		if !secretstore.Exists(dbPath) {
			return fmt.Errorf("no secret store at %s", dbPath)
		}
		db, _, err := secretstore.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := db.Delete(name); err != nil {
			return err
		}
		fmt.Printf("removed secret %s\n", name)
		return nil
	})
}

func secretLsCommand() *ff.Command {
	var dbPath string
	return newCommand("ls", "hsh secret ls [--db PATH]", "list stored secret names (never values)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", secretstore.DefaultPath(), "path to the secret store")
	}, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("secret ls takes no arguments\n\n%s", secretUsage)
		}
		if !secretstore.Exists(dbPath) {
			fmt.Println("no secrets stored")
			return nil
		}
		db, _, err := secretstore.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		entries, err := db.List()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("no secrets stored")
			return nil
		}
		fmt.Printf("%-30s %-20s %-20s %-20s %s\n", "NAME", "KIND", "CREATED", "UPDATED", "HOSTS / LOGIN")
		for _, e := range entries {
			kind, detail := "static", strings.Join(e.Hosts, ",")
			if detail == "" {
				detail = "(no hosts: hsh secret allow " + e.Name + " HOST)"
			}
			if e.Kind == "oauth" {
				kind = "oauth:" + e.Provider
				detail = "expires " + shortTime(e.ExpiresAt)
				if e.Failure != "" {
					detail = "FAILED: " + e.Failure
				}
			}
			fmt.Printf("%-30s %-20s %-20s %-20s %s\n", e.Name, kind, shortTime(e.CreatedAt), shortTime(e.UpdatedAt), detail)
		}
		return nil
	})
}

func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339)
}
