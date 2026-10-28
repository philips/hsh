package cli

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyDetach(ch chan os.Signal) {
	signal.Notify(ch, syscall.SIGUSR1)
}
