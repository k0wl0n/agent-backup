//go:build !windows
// +build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr configures process attributes for Unix systems
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
