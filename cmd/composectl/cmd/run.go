package composectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
	"strings"
)

type (
	runOptions struct {
		Apps map[string]bool
	}
)

func init() {
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "run <app name> [<app name>] | --apps <app list>; if empty (\"\") then run all apps",
		Long:  ``,
		Args:  cobra.ArbitraryArgs,
	}
	opts := runOptions{Apps: map[string]bool{}}
	runAppShortlist := runCmd.Flags().String("apps", ",", "Comma separated list of apps to run;"+
		" all installed apps are started if not defined")

	runCmd.Run = func(cmd *cobra.Command, args []string) {
		if *runAppShortlist == "," && len(args) == 0 {
			DieNotNil(fmt.Errorf("at least one app must be specified as an argument or in `--apps` parameter"))
		}
		if len(*runAppShortlist) > 0 && *runAppShortlist != "," {
			for _, a := range strings.Split(*runAppShortlist, ",") {
				opts.Apps[a] = true
			}
		} else {
			for _, a := range args {
				opts.Apps[a] = true
			}
		}

		runApps(cmd, &opts)
	}
	rootCmd.AddCommand(runCmd)
}

func runApps(cmd *cobra.Command, opts *runOptions) {
	apps, err := compose.ListApps(cmd.Context(), config)
	DieNotNil(err)

	checkedApps := map[string]compose.App{}
	for _, app := range apps {
		appName := app.Name()
		if len(opts.Apps) > 0 && !opts.Apps[appName] {
			continue
		}
		if _, ok := checkedApps[appName]; ok {
			DieNotNil(fmt.Errorf("cannot start %s since there are two or more versions of it found in the store", appName))
		}
		checkedApps[appName] = app
	}
	for app := range opts.Apps {
		if _, ok := checkedApps[app]; !ok {
			DieNotNil(fmt.Errorf("specified app is not present in the local store: %s", app))
		}
	}

	var appURIs []string
	// Make sure all apps are installed before starting any of them
	for _, app := range checkedApps {
		err = compose.Install(cmd.Context(), config, app.Ref().String(),
			compose.WithInstallProgress(update.GetInstallProgressPrinter()))
		DieNotNil(err)
		appURIs = append(appURIs, app.Ref().String())
	}

	err = compose.StartApps(cmd.Context(), config, appURIs, compose.WithVerboseStart(true),
		compose.WithStartProgressHandler(func(app compose.App, status compose.AppStartStatus, any interface{}) {
			switch status {
			case compose.AppStartStatusStarting:
				fmt.Printf("Starting %s --> %s\n", app.Name(), app.Ref().String())
			case compose.AppStartStatusStarted:
				fmt.Printf("%s has been successfully started\n", app.Name())
			case compose.AppStartStatusFailed:
				fmt.Printf("failed to start %s\n", app.Name())
			}
		}))
	DieNotNil(err)
}
