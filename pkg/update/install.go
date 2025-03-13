package update

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"path"
)

func (u *runnerImpl) install(
	ctx context.Context,
	b *bucket,
	options ...compose.InstallOption) (err error) {

	if !(u.State == StateFetched || u.State == StateInstalling || u.State == StateInstalled) {
		return fmt.Errorf("cannot install update when it is in state '%s'", u.State.String())
	}

	defer func() {
		if storeErr := b.write(&u.Update); storeErr != nil {
			// TODO: replace it by using logger
			fmt.Printf("failed to save update state: %v", storeErr)
		}
	}()

	cs, err := v1.NewAppStore(u.config.StoreRoot, u.config.Platform)
	if err != nil {
		return err
	}

	u.State = StateInstalling
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
		err = compose.Install(ctx, app, cs, path.Join(u.config.StoreRoot, "blobs/sha256"), u.config.ComposeRoot,
			u.config.DockerHost, options...)
		if err != nil {
			return err
		}
	}

	u.Progress = 100
	u.State = StateInstalled

	return nil
}
