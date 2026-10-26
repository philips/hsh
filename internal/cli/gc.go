package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/denieddb"
)

func gcCommand() *ff.Command {
	return newCommand("gc", "hsh gc", "remove session state directories whose pods are gone", nil, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("gc takes no positional arguments")
		}
		base := denieddb.SessionsDir()
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("no session state to collect")
				return nil
			}
			return err
		}
		removed := 0
		for _, e := range entries {
			id := e.Name()
			if id == "" {
				continue
			}
			if podExists("hsh-" + id) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(base, id)); err != nil {
				return err
			}
			fmt.Printf("removed session state %s\n", id)
			removed++
		}
		if removed == 0 {
			fmt.Println("no orphaned session state")
		} else {
			fmt.Printf("removed %d session state director%s\n", removed, pluralY(removed))
		}
		return nil
	})
}
