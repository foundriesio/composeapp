package composectl

import (
	"fmt"

	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	runOptions struct {
		Apps []string
	}
)

func init() {
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "run <app name> [<app name>] | --apps <app list>; if empty (\"\") then run all apps",
		Long:  ``,
		Args:  cobra.ArbitraryArgs,
	}
	opts := runOptions{}
	runCmd.Flags().StringSliceVar(&opts.Apps, "apps", nil, "Comma-separated list of apps to run")

	runCmd.Run = func(cmd *cobra.Command, args []string) {
		if len(opts.Apps) > 0 && len(args) > 0 {
			DieNotNil(fmt.Errorf("cannot use both app list in `--apps` parameter and as arguments"))
		}
		if len(args) > 0 {
			opts.Apps = args
		}
		runApps(cmd, &opts)
	}
	rootCmd.AddCommand(runCmd)
}

func runApps(cmd *cobra.Command, opts *runOptions) {
	appURIs := checkUserListedApps(cmd.Context(), config, opts.Apps, true)

	// Make sure all apps are installed before starting any of them
	for _, appURI := range appURIs {
		DieNotNil(compose.Install(cmd.Context(), config, appURI,
			compose.WithInstallProgress(update.GetInstallProgressPrinter())))
	}

	DieNotNil(compose.StartApps(cmd.Context(), config, appURIs, compose.WithVerboseStart(true),
		compose.WithStartProgressHandler(func(app compose.App, status compose.AppStartStatus, any interface{}) {
			switch status {
			case compose.AppStartStatusStarting:
				fmt.Printf("Starting %s --> %s\n", app.Name(), app.Ref().String())
			case compose.AppStartStatusStarted:
				fmt.Printf("%s has been successfully started\n", app.Name())
			case compose.AppStartStatusFailed:
				fmt.Printf("failed to start %s\n", app.Name())
			}
		})))
}
