package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/settings"
)

func proxyHoldCommand() *ff.Command {
	var session string
	return newCommand("hold", "hsh proxy hold [FLAGS] [on | off | default | DURATION]",
		"show or change how long a session's proxy holds denied requests for approval", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "the session (default: this workspace's attached or only session)")
		}, func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("proxy hold takes at most one argument: on, off, default, or a duration such as 45s")
			}
			if session == "" {
				id, err := resolveSessionArg(nil)
				if err != nil {
					return fmt.Errorf("%w (or pass --session)", err)
				}
				session = id
			}
			session, err := resolveSessionRef(session)
			if err != nil {
				return err
			}
			cfgPath, err := sessionConfigPath(session)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				value := args[0]
				switch strings.ToLower(value) {
				case "on":
					value = config.SuggestedApprovalWait.String()
				case "default":
					value = ""
				case "off":
					value = "off"
				}
				if err := config.SetApprovalWait(cfgPath, value); err != nil {
					return err
				}
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			fmt.Printf("session %s: %s\n", session, describeHold(cfg.Network, effectiveApprovalWaitForSession(cfgPath)))
			return nil
		})
}

func describeHold(n config.Network, effective time.Duration) string {
	switch {
	case n.ApprovalWait != "":
		if d := n.ApprovalWaitDuration(); d == 0 {
			return "denied requests fail immediately (holding is off)"
		} else {
			return fmt.Sprintf("denied requests are held for up to %s while someone is watching", d)
		}
	case effective == 0:
		return fmt.Sprintf("denied requests fail immediately (holding is off; the default from %s)", settings.Path())
	default:
		return fmt.Sprintf("denied requests are held for up to %s while someone is watching (the default from %s)", effective, settings.Path())
	}
}
