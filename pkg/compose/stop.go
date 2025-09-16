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
	for _, app := range status.Apps {
		if _, ok := status.NotInstalledCompose[app.Ref().Digest]; ok {
			// skip stopping apps with non-installed compose project
			continue
		}
		cmd := exec.Command("docker", "compose", "down")
		cmd.Dir = cfg.GetAppComposeDir(app.Name())
		if _, err := cmd.CombinedOutput(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("failed to stop %s: %s; %s", app, exitErr.Error(), string(exitErr.Stderr))
			}
			return err
		}
	}
	return nil
}
