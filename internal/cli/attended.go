package cli

import (
	"os"
	"time"

	"hsh/internal/attend"
	"hsh/internal/monitordb"
	"hsh/internal/state"
)

func touchSessions(f monitordb.Filter) {
	entries, err := os.ReadDir(state.SessionsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if f.Session != "" && e.Name() != f.Session {
			continue
		}
		dir := state.SessionDir(e.Name())
		if f.Workspace != "" && readWorkspaceFile(dir) != f.Workspace {
			continue
		}
		_ = attend.Touch(attend.Path(dir))
	}
}

func watchSessions(f monitordb.Filter) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		touchSessions(f)
		t := time.NewTicker(attend.TouchEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				touchSessions(f)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
