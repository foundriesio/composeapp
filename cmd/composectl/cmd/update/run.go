package updatectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "run",
		Long:  ``,
	}
)

type (
	runOptions struct {
	}
)

func init() {
	opts := runOptions{}

	runCmd.Run = func(cmd *cobra.Command, args []string) {
		runUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(runCmd)
}

func runUpdateCmd(cmd *cobra.Command, args []string, opts *runOptions) {
	cfg, err := compose.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Run(cmd.Context())
	ExitIfNotNil(err)
}
