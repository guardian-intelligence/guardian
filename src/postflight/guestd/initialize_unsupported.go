//go:build !linux

package guestd

import (
	"context"
	"errors"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/guestproto"
)

func InitializeRestoredTurbo(context.Context, guestproto.Prepare, func()) error {
	return errors.New("restored Turbo initialization requires Linux")
}
