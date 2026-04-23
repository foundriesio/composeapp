package composectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/spf13/cobra"
)

type (
	uninstallOptions struct {
		ignoreNonInstalled bool
		prune              bool
	}
)

func init() {
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "uninstall <app-name-or-URI> [<app-name-or-URI>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
	}
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
	appURIs := checkUserListedApps(cmd.Context(), config, args, !opts.ignoreNonInstalled, true)
	DieNotNil(compose.UninstallApps(cmd.Context(), config, appURIs))
}
