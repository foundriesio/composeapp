package updatectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

var (
	completeCmd = &cobra.Command{
		Use:   "complete",
		Short: "complete",
		Long:  ``,
	}
)

type (
	completeOptions struct {
	}
)

func init() {
	opts := completeOptions{}

	completeCmd.Run = func(cmd *cobra.Command, args []string) {
		completeUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(completeCmd)
}

func completeUpdateCmd(cmd *cobra.Command, args []string, opts *completeOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Complete(cmd.Context())
	ExitIfNotNil(err)
}
