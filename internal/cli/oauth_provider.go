package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/oauthprov"
	"hsh/internal/secretstore"
)

const oauthProvidersLabel = "org.ifup.hsh.oauth-providers"

func oauthProvidersDir() string {
	return filepath.Join(secretstore.ConfigDir(), "oauth-providers")
}

func oauthProviderCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "provider",
		Usage:     "hsh oauth provider SUBCOMMAND",
		ShortHelp: "list, show, and override OAuth provider descriptors",
		Flags:     ff.NewFlagSet("provider"),
		Subcommands: []*ff.Command{
			oauthProviderLsCommand(),
			oauthProviderCatCommand(),
			oauthProviderEditCommand(),
			oauthProviderAddCommand(),
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func oauthProviderLsCommand() *ff.Command {
	return newCommand("ls", "hsh oauth provider ls",
		"list OAuth providers: built into hsh, overridden, or added by you", nil,
		func(ctx context.Context, args []string) error {
			dir := oauthProvidersDir()
			names, err := oauthprov.Names(dir)
			if err != nil {
				return err
			}
			fmt.Printf("%-16s %-10s %s\n", "NAME", "SOURCE", "DETAILS")
			for _, n := range names {
				l, err := oauthprov.Load(dir, n)
				if err != nil {
					fmt.Printf("%-16s %-10s %v\n", n, "error", err)
					continue
				}
				source := string(l.Sources[0].Kind)
				if l.Sources[0].Kind == oauthprov.SourceOverride {
					if _, shipped := oauthprov.Shipped(n); !shipped {
						source = "user"
					}
				}
				details := []string{l.Descriptor.TokenEndpoint.Host + l.Descriptor.TokenEndpoint.Path}
				if d := len(l.Sources) - 1; d > 0 {
					details = append(details, fmt.Sprintf("%d drop-in(s)", d))
				}
				if len(l.Warnings) > 0 {
					details = append(details, "STALE: "+l.Warnings[0])
				}
				fmt.Printf("%-16s %-10s %s\n", n, source, strings.Join(details, "; "))
			}
			return nil
		})
}

func oauthProviderCatCommand() *ff.Command {
	var showShipped bool
	return newCommand("cat", "hsh oauth provider cat [FLAGS] NAME",
		"show a provider descriptor after overrides and drop-ins, and where each layer came from", func(fs *ff.FlagSet) {
			fs.BoolVar(&showShipped, 0, "shipped", "show the descriptor built into hsh, ignoring your overrides")
		}, func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("oauth provider cat takes exactly one provider name")
			}
			name := args[0]
			if showShipped {
				data, ok := oauthprov.Shipped(name)
				if !ok {
					return fmt.Errorf("hsh ships no oauth provider %q", name)
				}
				fmt.Printf("# %s (built into hsh, %s)\n%s", name, oauthprov.Hash(data), data)
				return nil
			}
			l, err := oauthprov.Load(oauthProvidersDir(), name)
			if err != nil {
				return err
			}
			for _, s := range l.Sources {
				fmt.Printf("# %s: %s\n", s.Kind, s.Path)
			}
			for _, w := range l.Warnings {
				fmt.Printf("# warning: %s\n", w)
			}
			fmt.Printf("%s\n", l.Merged)
			return nil
		})
}

func oauthProviderEditCommand() *ff.Command {
	var full bool
	var dropIn string
	return newCommand("edit", "hsh oauth provider edit [FLAGS] NAME",
		"override a provider with a drop-in (default) or a full copy (--full), in $EDITOR", func(fs *ff.FlagSet) {
			fs.BoolVar(&full, 0, "full", "edit a full copy of the descriptor instead of a drop-in (records the built-in descriptor's hash as \"base\")")
			fs.StringVar(&dropIn, 0, "drop-in", "50-local.json", "drop-in file name inside NAME.d/")
		}, func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("oauth provider edit takes exactly one provider name")
			}
			name := args[0]
			if err := oauthprov.ValidName(name); err != nil {
				return err
			}
			dir := oauthProvidersDir()
			path, err := prepareProviderEdit(dir, name, full, dropIn)
			if err != nil {
				return err
			}
			if err := runEditor(path); err != nil {
				return err
			}
			l, err := oauthprov.Load(dir, name)
			if err != nil {
				return fmt.Errorf("%s was saved but the provider does not load: %w", path, err)
			}
			for _, w := range l.Warnings {
				fmt.Fprintf(os.Stderr, "hsh: warning: %s\n", w)
			}
			fmt.Printf("%s: provider %q loads (%d layer(s)); running sessions pick it up on their next configuration reload\n", path, name, len(l.Sources))
			return nil
		})
}

func prepareProviderEdit(dir, name string, full bool, dropIn string) (string, error) {
	if full {
		path := oauthprov.OverridePath(dir, name)
		if fileExists(path) {
			return path, nil
		}
		content, err := fullCopy(name)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return path, os.WriteFile(path, content, 0o644)
	}
	if strings.ContainsAny(dropIn, "/\\") || !strings.HasSuffix(dropIn, ".json") {
		return "", fmt.Errorf("--drop-in %q must be a plain file name ending in .json", dropIn)
	}
	if _, shipped := oauthprov.Shipped(name); !shipped && !fileExists(oauthprov.OverridePath(dir, name)) {
		return "", fmt.Errorf("hsh ships no oauth provider %q, so there is nothing for a drop-in to change; create it with: hsh oauth provider edit --full %s", name, name)
	}
	path := filepath.Join(oauthprov.DropInDir(dir, name), dropIn)
	if fileExists(path) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte("{}\n"), 0o644)
}

func fullCopy(name string) ([]byte, error) {
	data, shipped := oauthprov.Shipped(name)
	if !shipped {
		skeleton := map[string]any{
			"version":              oauthprov.FormatVersion,
			"token_endpoint":       map[string]string{"host": "auth.example.com", "path": "/oauth/token"},
			"response_fields":      []string{"access_token", "refresh_token"},
			"request_nonce_fields": []string{"refresh_token"},
			"api":                  map[string]any{"hosts": []string{"api.example.com"}},
		}
		out, err := json.MarshalIndent(skeleton, "", "  ")
		return append(out, '\n'), err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	doc["base"] = oauthprov.Hash(data)
	out, err := json.MarshalIndent(doc, "", "  ")
	return append(out, '\n'), err
}

func runEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	fields := strings.Fields(editor)
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %s: %w", editor, err)
	}
	return nil
}

func oauthProviderAddCommand() *ff.Command {
	var image string
	var yes, force bool
	return newCommand("add", "hsh oauth provider add --from-image IMAGE [FLAGS] [NAME...]",
		"review provider descriptors an image suggests and copy the ones you accept into your config", func(fs *ff.FlagSet) {
			fs.StringVar(&image, 0, "from-image", "", "image whose "+oauthProvidersLabel+" label suggests descriptors")
			fs.BoolVar(&yes, 'y', "yes", "accept without asking")
			fs.BoolVar(&force, 0, "force", "replace an existing descriptor file")
		}, func(ctx context.Context, args []string) error {
			if image == "" {
				return fmt.Errorf("oauth provider add needs --from-image IMAGE")
			}
			out, err := podman("image", "inspect", "--format", `{{index .Labels "`+oauthProvidersLabel+`"}}`, image)
			if err != nil {
				return fmt.Errorf("inspect image %s: %w\n%s", image, err, out)
			}
			return addSuggestedProviders(oauthProvidersDir(), strings.TrimSpace(out), args, yes, force, os.Stdin, os.Stdout)
		})
}

func addSuggestedProviders(dir, label string, only []string, yes, force bool, in io.Reader, out io.Writer) error {
	if label == "" || label == "<no value>" {
		return fmt.Errorf("the image has no %s label", oauthProvidersLabel)
	}
	var suggested map[string]json.RawMessage
	if err := json.Unmarshal([]byte(label), &suggested); err != nil {
		return fmt.Errorf("%s label: %w", oauthProvidersLabel, err)
	}
	names := only
	if len(names) == 0 {
		for n := range suggested {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	reader := bufio.NewReader(in)
	added := 0
	for _, name := range names {
		raw, ok := suggested[name]
		if !ok {
			return fmt.Errorf("the image suggests no provider %q", name)
		}
		if err := oauthprov.ValidName(name); err != nil {
			return err
		}
		if _, err := oauthprov.Parse(raw); err != nil {
			return fmt.Errorf("suggested provider %q: %w", name, err)
		}
		path := oauthprov.OverridePath(dir, name)
		if fileExists(path) && !force {
			return fmt.Errorf("%s already exists (use --force to replace it)", path)
		}
		var pretty any
		_ = json.Unmarshal(raw, &pretty)
		body, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Fprintf(out, "provider %q suggested by the image:\n%s\n", name, body)
		if _, shipped := oauthprov.Shipped(name); shipped {
			fmt.Fprintf(out, "note: this replaces the %q descriptor built into hsh\n", name)
		}
		if !yes {
			fmt.Fprintf(out, "copy it to %s? [y/N] ", path)
			answer, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				fmt.Fprintf(out, "skipped %q\n", name)
				continue
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
			return err
		}
		added++
		fmt.Fprintf(out, "added %s\n", path)
	}
	if added == 0 {
		fmt.Fprintln(out, "no providers added")
	}
	return nil
}
