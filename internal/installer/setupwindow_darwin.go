//go:build darwin

package installer

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// platformSetupWindow opens the setup script in Terminal, or is nil where open does not exist.
func platformSetupWindow() windowStarter {
	if _, err := exec.LookPath("open"); err != nil {
		return nil
	}
	return terminalWindow(func(ctx context.Context, script string) error {
		out, err := commandContext(ctx, "open", "-a", "Terminal", script).CombinedOutput()
		if err != nil {
			return fmt.Errorf("open -a Terminal: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	})
}
