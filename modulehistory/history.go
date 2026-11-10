// Package modulehistory tracks installation facts independently of enablement.
package modulehistory

import (
	"context"
	"errors"
)

type State string

const (
	Unknown        State = "unknown"
	NeverInstalled State = "never_installed"
	Installed      State = "installed"
)

var ErrUnavailable = errors.New("module_history_unavailable")

// Repository must retain installed facts across restarts and configuration changes.
// Only explicit initialization of a new inventory can certify never-installed.
type Repository interface {
	State(context.Context, string) (State, error)
	ObserveInstalled(context.Context, []string) error
}
