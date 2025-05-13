package compose

import (
	"context"
	"fmt"
	"os/exec"
)

func StopApps(ctx context.Context, cfg *Config, appRefs []string) error {
	status, err := CheckAppsStatus(ctx, cfg, appRefs)
	if err != nil {
		return err
	}
	if !status.AreInstalled() {
		return fmt.Errorf("cannot stop not installed app(s)")
	}
	for _, app := range status.Apps {
		cmd := exec.Command("docker", "compose", "down")
		cmd.Dir = cfg.GetAppComposeDir(app.Name())
		_, err := cmd.CombinedOutput()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("failed to stop %s: %s; %s", app, exitErr.Error(), string(exitErr.Stderr))
			}
			return err
		}
	}
	return nil
}
