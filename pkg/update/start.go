package update

import (
	"context"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
)

func (u *runnerImpl) run(ctx context.Context, b *session) error {
	if len(u.URIs) == 0 {
		u.Progress = 100
		return nil
	}
	progressStep := 100 / len(u.URIs)
	if err := compose.StartApps(ctx, u.config, u.URIs,
		compose.WithVerboseStart(false),
		compose.WithStartProgressHandler(func(app compose.App, status compose.AppStartStatus, any interface{}) {
			switch status {
			case compose.AppStartStatusStarting:
				fmt.Printf("\tstarting %s --> %s ... ", app.Name(), app.Ref().String())
			case compose.AppStartStatusStarted:
				fmt.Println("done")
				u.Progress += progressStep
			case compose.AppStartStatusFailed:
				fmt.Println("failed")
				u.Progress += progressStep
			}
		}),
	); err != nil {
		return err
	}
	return nil
}
