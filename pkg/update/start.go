package update

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"os"
	"os/exec"
	"path"
)

func (u *runnerImpl) run(ctx context.Context, b *session) error {
	cs, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform)
	if err != nil {
		return err
	}

	apps := map[string]compose.App{}
	for _, appURI := range u.URIs {
		app, err := u.config.AppLoader.LoadAppTree(ctx, cs, platforms.OnlyStrict(u.config.Platform), appURI)
		if err != nil {
			return err
		}
		apps[appURI] = app
	}

	for _, app := range apps {
		fmt.Printf("Starting %store --> %store\n", app.Name(), app.Ref().String())
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = path.Join(u.config.ComposeRoot, app.Name())
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		err = cmd.Run()
		if err != nil {
			return err
		}
	}

	u.Progress = 100
	u.State = StateCompleted

	return nil
}
