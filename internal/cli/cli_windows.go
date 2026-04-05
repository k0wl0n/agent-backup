package cli

import (
	"os/exec"
)

// setSysProcAttr is a no-op on Windows
func setSysProcAttr(cmd *exec.Cmd) {
	// No-op on Windows
}
