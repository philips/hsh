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
  hsh secret add NAME [--db PATH] [--force] [--value-stdin]
  hsh secret rm NAME [--db PATH]
  hsh secret ls [--db PATH]

Secrets are stored unencrypted in the hsh secret store (default
$XDG_CONFIG_HOME/hsh/secrets.db, mode 0600), readable only by your user —
the same trust level as ~/.netrc or ~/.aws/credentials. Config files
reference them as "hsh://NAME".
`

const firstUseWarning = `hsh: note: secrets are stored unencrypted in %s
(readable only by your user — the same trust level as ~/.netrc or ~/.aws/credentials)`

func secretCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "secret",
		Usage:     "hsh secret SUBCOMMAND",
		ShortHelp: "manage the hsh secret store (add/rm/ls)",
		Flags:     ff.NewFlagSet("secret"),
		Subcommands: []*ff.Command{
			secretAddCommand(),
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
	return newCommand("add", "hsh secret add [FLAGS] NAME", "store a secret (hidden prompt, or --value-stdin)", func(fs *ff.FlagSet) {
		fs.StringVar(&dbPath, 0, "db", secretstore.DefaultPath(), "path to the secret store")
		fs.BoolVar(&force, 0, "force", "overwrite an existing secret")
		fs.BoolVar(&valueStdin, 0, "value-stdin", "read the value from stdin (for scripting)")
	}, func(ctx context.Context, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("secret add requires exactly one NAME\n\n%s", secretUsage)
		}
		name := args[0]
		if name == "" {
			return fmt.Errorf("secret name must not be empty")
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
		fmt.Printf("stored secret %s\n", name)
		return nil
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
		fmt.Printf("%-30s %-20s %-20s %-20s %s\n", "NAME", "KIND", "CREATED", "UPDATED", "LOGIN")
		for _, e := range entries {
			kind, login := "static", ""
			if e.Kind == "oauth" {
				kind = "oauth:" + e.Provider
				login = "expires " + shortTime(e.ExpiresAt)
				if e.Failure != "" {
					login = "FAILED: " + e.Failure
				}
			}
			fmt.Printf("%-30s %-20s %-20s %-20s %s\n", e.Name, kind, shortTime(e.CreatedAt), shortTime(e.UpdatedAt), login)
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
