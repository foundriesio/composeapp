package composectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

func init() {
	installCmd := &cobra.Command{
		Use:   "install <ref>",
		Short: "install <ref>",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
		Run:   installApp,
	}
	rootCmd.AddCommand(installCmd)
}

func installApp(cmd *cobra.Command, args []string) {
	DieNotNil(compose.Install(cmd.Context(), config, args[0],
		compose.WithInstallProgress(update.GetInstallProgressPrinter())))
}
