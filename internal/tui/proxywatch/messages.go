package proxywatch

import (
	"time"

	"hsh/internal/denieddb"
)

const refreshInterval = 2 * time.Second

type tickMsg time.Time

type refreshMsg struct {
	pending []denieddb.DeniedHost
	ignored []denieddb.DeniedHost
	err     error
}

type action int

const (
	actionApprove action = iota
	actionIgnore
)

type actionMsg struct {
	kind  action
	hosts []string
	err   error
}
