//go:build !unix && !windows

package pipeline

import (
	"errors"
	"os/exec"
)

func detachBackgroundProcess(*exec.Cmd) error {
	return errors.New("detached pipeline execution is not supported on this platform")
}
