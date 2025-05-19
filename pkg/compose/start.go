package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"os/exec"
)

func StartApps(ctx context.Context, cfg *Config, appURIs []string) error {
	cs, err := cfg.AppStoreFactory()
	if err != nil {
		return err
	}

	apps := map[string]App{}
	for _, appURI := range appURIs {
		app, err := cfg.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(cfg.Platform), appURI)
		if err != nil {
			return err
		}
		apps[appURI] = app
	}

	for _, app := range apps {
		// TODO: use the docker compose API to start apps
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = cfg.GetAppComposeDir(app.Name())
		if _, err := cmd.CombinedOutput(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("failed to start %s: %s; %s", app, exitErr.Error(), string(exitErr.Stderr))
			}
			return err
		}
	}
	return nil
}
