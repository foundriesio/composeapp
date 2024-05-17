package composectl

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "uninstall apps",
	Long:  ``,
	Args:  cobra.MinimumNArgs(1),
}

type (
	uninstallOptions struct {
		ignoreNonInstalled bool
	}
)

func init() {
	opts := uninstallOptions{}
	uninstallCmd.Flags().BoolVar(&opts.ignoreNonInstalled, "ignore-non-installed", false,
		"Do not yield error if app installation is not found")
	uninstallCmd.Run = func(cmd *cobra.Command, args []string) {
		uninstallApps(cmd, args, &opts)
	}
	rootCmd.AddCommand(uninstallCmd)
}

func uninstallApps(cmd *cobra.Command, args []string, opts *uninstallOptions) {
	apps := getAllAppStatuses(cmd.Context())
	for _, app := range args {
		if _, ok := apps[app]; ok {
			DieNotNil(fmt.Errorf("cannot uninstall running app: %s", app))
		}
		appComposeDir := filepath.Join(config.ComposeRoot, app)
		if !opts.ignoreNonInstalled {
			if _, err := os.Stat(appComposeDir); os.IsNotExist(err) {
				DieNotNil(fmt.Errorf("app is not installed: %s", app))
			}
		}
		DieNotNil(os.RemoveAll(appComposeDir))
	}
}
