package updatectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	installOptions struct{}
)

func init() {
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install the updated apps",
		Long:  `Install the updated apps by extracting the compose project and loading its images into the Docker image storage	`,
	}

	opts := installOptions{}

	installCmd.Run = func(cmd *cobra.Command, args []string) {
		installUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(installCmd)
}

func installUpdateCmd(cmd *cobra.Command, args []string, opts *installOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Install(cmd.Context(), compose.WithInstallProgress(update.GetInstallProgressPrinter()))
	ExitIfNotNil(err)
}
