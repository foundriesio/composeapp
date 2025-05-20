package update

import (
	"context"
	"github.com/foundriesio/composeapp/pkg/compose"
)

func (u *runnerImpl) install(
	ctx context.Context,
	b *session,
	options ...compose.InstallOption) (err error) {
	if u.LoadedImages == nil {
		u.LoadedImages = make(map[string]struct{})
	}
	options = append(options, compose.WithLoadedImages(u.LoadedImages))
	for _, appURI := range u.URIs {
		err = compose.Install(ctx, u.config, appURI, options...)
		if err != nil {
			return err
		}
	}

	// TODO: update installation progress conducted in compose.Install
	u.Progress = 100
	return nil
}
