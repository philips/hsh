package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if s.NetworkProxyApprovalWait == "" {
		return config.DefaultApprovalWait, nil
	}
	d, err := config.ParseApprovalWait(s.NetworkProxyApprovalWait)
	if err != nil {
		return config.DefaultApprovalWait, err
	}
	return d, nil
}

func globalSuspendDelay() (time.Duration, error) {
	s, err := settings.Load(settings.Path())
	if err != nil {
		return config.DefaultSuspendDelay, err
	}
	if s.NetworkProxyApprovalSuspendDelay == "" {
		return config.DefaultSuspendDelay, nil
	}
	d, err := config.ParseSuspendDelay(s.NetworkProxyApprovalSuspendDelay)
	if err != nil {
		return config.DefaultSuspendDelay, err
	}
	return d, nil
}

func effectiveApprovalWaitForSession(cfgPath string) time.Duration {
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Network.ApprovalWait != "" {
		return cfg.Network.ApprovalWaitDuration()
	}
	d, _ := globalApprovalWait()
	return d
}

func effectiveSuspendDelayForSession(cfgPath string) time.Duration {
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Network.SuspendDelay != "" {
		return cfg.Network.SuspendDelayDuration()
	}
	d, _ := globalSuspendDelay()
	return d
}

var settingsTemplate = `{
  "monitor_key": "",
  "network_proxy_approval_wait": "",
  "network_proxy_approval_suspend_delay": ""
}
`

type sessionSettingsDoc struct {
	ApprovalWait string `json:"approval_wait"`
	SuspendDelay string `json:"suspend_delay"`
}

func validateSettings(s settings.Settings) error {
	if s.MonitorKey != "" {
		if _, err := relay.ParseKey(s.MonitorKey); err != nil {
			return fmt.Errorf("monitor_key: %w", err)
		}
	}
	if _, err := config.ParseApprovalWait(s.NetworkProxyApprovalWait); err != nil {
		return err
	}
	if _, err := config.ParseSuspendDelay(s.NetworkProxyApprovalSuspendDelay); err != nil {
		return err
	}
	return nil
}

func describeSettings(s settings.Settings, out *strings.Builder) {
	fmt.Fprintf(out, "settings file: %s\n", settings.Path())
	key := s.MonitorKey
	if key == "" {
		key = config.DefaultMonitorKey
	}
	fmt.Fprintf(out, "monitor_key: %s\n", key)
	wait, waitNote := s.NetworkProxyApprovalWait, "builtin default"
	if wait == "" {
		wait = config.DefaultApprovalWait.String()
	}
	fmt.Fprintf(out, "network_proxy_approval_wait: %s (%s; holds denied requests for approval this long)\n", wait, waitNote)
	delay, delayNote := s.NetworkProxyApprovalSuspendDelay, "builtin default"
	if delay == "" {
		delay = config.DefaultSuspendDelay.String()
	}
	fmt.Fprintf(out, "network_proxy_approval_suspend_delay: %s (%s; suspends the agent this long into a hold; off never suspends)\n", delay, delayNote)
}

func describeSessionSettings(session, cfgPath string, out *strings.Builder) {
	wait := effectiveApprovalWaitForSession(cfgPath)
	waitText, waitNote := wait.String(), "builtin default"
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Network.ApprovalWait != "" {
		waitNote = "session override"
		if wait <= 0 {
			waitText = "off"
		}
	}
	delay := effectiveSuspendDelayForSession(cfgPath)
	delayText, delayNote := delay.String(), "builtin default"
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Network.SuspendDelay != "" {
		delayNote = "session override"
		if delay <= 0 {
			delayText = "off"
		}
	}
	fmt.Fprintf(out, "session %s: approval_wait %s (%s), suspend_delay %s (%s)\n", session, waitText, waitNote, delayText, delayNote)
}

func editSettingsOnce(path string) (settings.Settings, error) {
	if err := runEditor(path); err != nil {
		return settings.Settings{}, err
	}
	s, err := settings.Load(path)
	if err != nil {
		return settings.Settings{}, err
	}
	return s, validateSettings(s)
}

func editSessionSettingsOnce(session, cfgPath string) error {
	dir, err := os.MkdirTemp("", "hsh-session-settings-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "settings.json")
	doc := sessionSettingsDoc{}
	if cfg, err := config.Load(cfgPath); err == nil {
		doc.ApprovalWait = cfg.Network.ApprovalWait
		doc.SuspendDelay = cfg.Network.SuspendDelay
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "empty values fall back to the global defaults (hsh settings edit)")
	if err := runEditor(path); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var edited sessionSettingsDoc
	if err := json.Unmarshal(raw, &edited); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := config.ParseApprovalWait(edited.ApprovalWait); err != nil {
		return err
	}
	if _, err := config.ParseSuspendDelay(edited.SuspendDelay); err != nil {
		return err
	}
	if err := config.SetApprovalWait(cfgPath, edited.ApprovalWait); err != nil {
		return err
	}
	if err := config.SetSuspendDelay(cfgPath, edited.SuspendDelay); err != nil {
		return err
	}
	var b strings.Builder
	describeSessionSettings(session, cfgPath, &b)
	fmt.Print(b.String())
	return nil
}

func editAgain(err error) bool {
	if !isTerminal(os.Stdin) {
		return false
	}
	fmt.Fprintf(os.Stderr, "hsh: %v\nedit again? [Y/n] ", err)
	var answer string
	if _, readErr := fmt.Fscanln(os.Stdin, &answer); readErr != nil {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(answer))
	return a != "n" && a != "no"
}

func settingsCommand() *ff.Command {
	var session string
	edit := newCommand("edit", "hsh settings edit [--session SESSION]",
		"edit global hsh settings in $EDITOR, or one session's approval and suspend overrides with --session", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "edit this session's approval and suspend overrides instead of the global settings (default: this workspace's attached or only session)")
		}, func(ctx context.Context, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("settings edit takes no arguments")
			}
			if session != "" {
				session, err := resolveSessionRef(session)
				if err != nil {
					return err
				}
				cfgPath, err := sessionConfigPath(session)
				if err != nil {
					return err
				}
				for {
					err := editSessionSettingsOnce(session, cfgPath)
					if err == nil {
						return nil
					}
					if !editAgain(err) {
						return err
					}
				}
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
				s, err := editSettingsOnce(path)
				if err == nil {
					var b strings.Builder
					describeSettings(s, &b)
					fmt.Print(b.String())
					return nil
				}
				if !editAgain(err) {
					return err
				}
			}
		})
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:        "settings",
		Usage:       "hsh settings SUBCOMMAND",
		ShortHelp:   "global hsh settings, and per-session overrides (apply everywhere unless overridden)",
		Flags:       ff.NewFlagSet("settings"),
		Subcommands: []*ff.Command{edit},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}
