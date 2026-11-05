package cli

import (
	"context"
	"fmt"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/state"
)

func renameCommand() *ff.Command {
	var clear bool
	return newCommand("rename", "hsh rename SESSION NAME | hsh rename --clear SESSION", "name a session, or change or remove its name", func(fs *ff.FlagSet) {
		fs.BoolVar(&clear, 0, "clear", "remove the session's name")
	}, func(ctx context.Context, args []string) error {
		want := 2
		if clear {
			want = 1
		}
		if len(args) != want {
			if clear {
				return fmt.Errorf("rename --clear takes exactly one session")
			}
			return fmt.Errorf("rename takes a session and a new name (to remove a name: hsh rename --clear SESSION)")
		}
		id, err := resolveSessionRef(args[0])
		if err != nil {
			return err
		}
		if !podExists("hsh-" + id) {
			return fmt.Errorf("no session %s (see: hsh ls)", args[0])
		}
		if clear {
			if err := state.WriteName(id, ""); err != nil {
				return err
			}
			fmt.Printf("session %s is unnamed\n", id)
			return nil
		}
		if err := validateSessionName(args[1], id); err != nil {
			return err
		}
		if err := state.WriteName(id, args[1]); err != nil {
			return err
		}
		fmt.Printf("session %s is now named %s\n", id, args[1])
		return nil
	})
}
