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

func (u *runnerImpl) run(ctx context.Context, b *bucket) error {
	if !(u.State == StateInstalled || u.State == StateRunning) {
		return fmt.Errorf("cannot run update when it is in state '%store'", u.State.String())
	}

	cs, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform)
	if err != nil {
		return err
	}

	defer func() {
		if storeErr := b.write(&u.Update); storeErr != nil {
			// TODO: replace it by using logger
			fmt.Printf("failed to save update state: %v", storeErr)
		}
	}()

	u.State = StateRunning
	u.Progress = 0
	if storeErr := b.write(&u.Update); storeErr != nil {
		return fmt.Errorf("failed to save update state: %w", storeErr)
	}

	apps := map[string]compose.App{}
	for _, appURI := range u.URIs {
		app, err := v1.NewAppLoader().LoadAppTree(ctx, cs, platforms.OnlyStrict(u.config.Platform), appURI)
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
