package cli

import (
	"os"
	"time"

	"hsh/internal/attend"
	"hsh/internal/denieddb"
)

func touchSessions(f denieddb.Filter) {
	entries, err := os.ReadDir(denieddb.SessionsDir())
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
		dir := denieddb.SessionDir(e.Name())
		if f.Workspace != "" && readWorkspaceFile(dir) != f.Workspace {
			continue
		}
		_ = attend.Touch(attend.Path(dir))
	}
}

func watchSessions(f denieddb.Filter) func() {
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
