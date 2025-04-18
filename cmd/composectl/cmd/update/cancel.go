package updatectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
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
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	err = updateCtl.Cancel(cmd.Context())
	ExitIfNotNil(err)
}
