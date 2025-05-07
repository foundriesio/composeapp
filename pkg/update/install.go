package update

import (
	"context"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"path"
)

func (u *runnerImpl) install(
	ctx context.Context,
	b *session,
	options ...compose.InstallOption) (err error) {

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

	if u.LoadedImages == nil {
		u.LoadedImages = make(map[string]struct{})
	}
	options = append(options, compose.WithLoadedImages(u.LoadedImages))
	for _, app := range apps {
		err = compose.Install(ctx, app, cs, path.Join(u.config.StoreRoot, "blobs/sha256"), u.config.ComposeRoot,
			u.config.DockerHost, options...)
		if err != nil {
			return err
		}
	}

	// TODO: update installation progress conducted in compose.Install
	u.Progress = 100
	return nil
}
