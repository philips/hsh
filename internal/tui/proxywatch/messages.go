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
	holds   map[string]string
	err     error
}

type holdMsg struct {
	session string
	summary string
	on      bool
	err     error
}

type action int

const (
	actionApprove action = iota
	actionIgnore
)

type actionMsg struct {
	kind action
	rows []denieddb.DeniedHost
	err  error
}
