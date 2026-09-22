//go:build !linux

package coordinator

import (
	"context"
	"errors"
)

func acquireFailoverProviderLock(context.Context, string) (func(), error) {
	return nil, errors.New("automatic global account recovery requires Linux process and credential proof")
}
