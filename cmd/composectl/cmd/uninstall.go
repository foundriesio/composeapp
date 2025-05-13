package composectl

import (
	"fmt"
	"github.com/docker/docker/api/types/filters"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/spf13/cobra"
	"os"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "uninstall <app-name> [<app-name>]",
	Long:  ``,
	Args:  cobra.MinimumNArgs(1),
}

type (
	uninstallOptions struct {
		ignoreNonInstalled bool
		prune              bool
	}
)

func init() {
	opts := uninstallOptions{}
	uninstallCmd.Flags().BoolVar(&opts.ignoreNonInstalled, "ignore-non-installed", false,
		"Do not yield error if app installation is not found")
	uninstallCmd.Flags().BoolVar(&opts.prune, "prune", false, "prune unused images in the docker store")
	uninstallCmd.Run = func(cmd *cobra.Command, args []string) {
		uninstallApps(cmd, args, &opts)
	}
	rootCmd.AddCommand(uninstallCmd)
}

func uninstallApps(cmd *cobra.Command, args []string, opts *uninstallOptions) {
	apps := getAllAppStatuses(cmd.Context(), false)
	for _, app := range args {
		if _, ok := apps[app]; ok {
			DieNotNil(fmt.Errorf("cannot uninstall running app: %s", app))
		}
		appComposeDir := config.GetAppComposeDir(app)
		if !opts.ignoreNonInstalled {
			if _, err := os.Stat(appComposeDir); os.IsNotExist(err) {
				DieNotNil(fmt.Errorf("app is not installed: %s", app))
			}
		}
		DieNotNil(os.RemoveAll(appComposeDir))
	}
	if opts.prune {
		cli, err := compose.GetDockerClient(dockerHost)
		DieNotNil(err)
		_, err = cli.ImagesPrune(cmd.Context(), filters.NewArgs(filters.Arg("dangling", "false")))
		DieNotNil(err)
	}
}
