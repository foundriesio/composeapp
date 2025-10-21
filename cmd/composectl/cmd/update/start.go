package updatectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	runOptions struct {
	}
)

func init() {
	runCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the updated apps",
		Long:  `Start the fetched and installed apps by launching their compose services`,
	}

	opts := runOptions{}

	runCmd.Run = func(cmd *cobra.Command, args []string) {
		runUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(runCmd)
}

func runUpdateCmd(cmd *cobra.Command, args []string, opts *runOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	ExitIfNotNil(updateCtl.Start(cmd.Context(), compose.WithVerboseStart(false),
		compose.WithStartProgressHandler(func(app compose.App, status compose.AppStartStatus, any interface{}) {
			switch status {
			case compose.AppStartStatusStarting:
				fmt.Printf("\tstarting %s --> %s ... ", app.Name(), app.Ref().String())
			case compose.AppStartStatusStarted:
				fmt.Println("done")
			case compose.AppStartStatusFailed:
				fmt.Println("failed")
			}
		})))
}
