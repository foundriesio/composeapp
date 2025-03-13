package updatectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

var (
	cancelCmd = &cobra.Command{
		Use:   "cancel",
		Short: "cancel",
		Long:  ``,
	}
)

type (
	cancelOptions struct {
	}
)

func init() {
	opts := cancelOptions{}

	cancelCmd.Run = func(cmd *cobra.Command, args []string) {
		cancelUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(cancelCmd)
}

func cancelUpdateCmd(cmd *cobra.Command, args []string, opts *cancelOptions) {
	cfg, err := compose.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Cancel(cmd.Context())
	ExitIfNotNil(err)
}
