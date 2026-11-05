package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/relay"
	"hsh/internal/settings"
)

func globalApprovalWait() (time.Duration, error) {
	s, err := settings.Load(settings.Path())
	if err != nil {
		return config.DefaultApprovalWait, err
	}
	if s.ApprovalWait == "" {
		return config.DefaultApprovalWait, nil
	}
	d, err := config.ParseApprovalWait(s.ApprovalWait)
	if err != nil {
		return config.DefaultApprovalWait, err
	}
	return d, nil
}

func globalFreezeDelay() (time.Duration, error) {
	s, err := settings.Load(settings.Path())
	if err != nil {
		return config.DefaultFreezeDelay, err
	}
	if s.FreezeDelay == "" {
		return config.DefaultFreezeDelay, nil
	}
	d, err := config.ParseFreezeDelay(s.FreezeDelay)
	if err != nil {
		return config.DefaultFreezeDelay, err
	}
	return d, nil
}

var settingsTemplate = `{
  "monitor_key": "",
  "approval_wait": "",
  "freeze_delay": ""
}
`

func validateSettings(s settings.Settings) error {
	if s.MonitorKey != "" {
		if _, err := relay.ParseKey(s.MonitorKey); err != nil {
			return fmt.Errorf("monitor_key: %w", err)
		}
	}
	if _, err := config.ParseApprovalWait(s.ApprovalWait); err != nil {
		return err
	}
	if _, err := config.ParseFreezeDelay(s.FreezeDelay); err != nil {
		return err
	}
	return nil
}

func describeSettings(s settings.Settings, out *strings.Builder) {
	fmt.Fprintf(out, "settings file: %s\n", settings.Path())
	key, keyNote := s.MonitorKey, "builtin default"
	if key == "" {
		key = config.DefaultMonitorKey
	}
	fmt.Fprintf(out, "monitor_key: %s (%s)\n", key, keyNote)
	wait, waitNote := s.ApprovalWait, "builtin default"
	if wait == "" {
		wait = config.DefaultApprovalWait.String()
	}
	fmt.Fprintf(out, "approval_wait: %s (%s; holds denied requests for approval this long)\n", wait, waitNote)
	delay, delayNote := s.FreezeDelay, "builtin default"
	if delay == "" {
		delay = config.DefaultFreezeDelay.String()
	}
	fmt.Fprintf(out, "freeze_delay: %s (%s; freezes the agent this long into a hold)\n", delay, delayNote)
}

func settingsCommand() *ff.Command {
	var cmd *ff.Command
	edit := newCommand("edit", "hsh settings edit", "edit global hsh settings in $EDITOR (monitor key, approval and freeze defaults)", nil, func(ctx context.Context, args []string) error {
		if len(args) != 0 {
			return fmt.Errorf("settings edit takes no arguments")
		}
		path := settings.Path()
		if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
			return err
		}
		if !fileExists(path) {
			if err := os.WriteFile(path, []byte(settingsTemplate), 0o644); err != nil {
				return err
			}
		}
		for {
			if err := runEditor(path); err != nil {
				return err
			}
			s, err := settings.Load(path)
			if err == nil {
				err = validateSettings(s)
			}
			if err == nil {
				var b strings.Builder
				describeSettings(s, &b)
				fmt.Print(b.String())
				return nil
			}
			if !isTerminal(os.Stdin) {
				return err
			}
			fmt.Fprintf(os.Stderr, "hsh: %v\nedit again? [Y/n] ", err)
			var answer string
			if _, readErr := fmt.Fscanln(os.Stdin, &answer); readErr != nil {
				return err
			}
			if a := strings.ToLower(strings.TrimSpace(answer)); a == "n" || a == "no" {
				return fmt.Errorf("settings unchanged")
			}
		}
	})
	cmd = &ff.Command{
		Name:         "settings",
		Usage:        "hsh settings SUBCOMMAND",
		ShortHelp:    "global hsh settings (apply to every session; a session's config overrides them)",
		Flags:        ff.NewFlagSet("settings"),
		Subcommands: []*ff.Command{edit},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func effectiveApprovalWaitForSession(cfgPath string) time.Duration {
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Network.ApprovalWait != "" {
		return cfg.Network.ApprovalWaitDuration()
	}
	d, _ := globalApprovalWait()
	return d
}
