package updatectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

var (
	runCmd = &cobra.Command{
		Use:   "start",
		Short: "Start the updated apps",
		Long:  `Start the fetched and installed applications by launching their compose services`,
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
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Start(cmd.Context())
	ExitIfNotNil(err)
}
