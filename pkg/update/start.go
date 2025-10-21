package update

import (
	"context"
	"github.com/foundriesio/composeapp/pkg/compose"
)

func (u *runnerImpl) start(ctx context.Context, b *session, options ...compose.StartOption) error {
	opts := compose.StartOptions{}
	for _, o := range options {
		o(&opts)
	}
	progressStep := 100 / len(u.URIs)
	startOptions := options
	// override the progress reporter if one is provided
	startOptions = append(startOptions,
		compose.WithStartProgressHandler(func(app compose.App, status compose.AppStartStatus, any interface{}) {
			if status == compose.AppStartStatusStarted || status == compose.AppStartStatusFailed {
				u.Progress += progressStep
			}
			// invoke the progress reporter if one is provided by a caller
			if opts.ProgressHandler != nil {
				opts.ProgressHandler(app, status, any)
			}
		}))
	return compose.StartApps(ctx, u.config, u.URIs, startOptions...)
}
