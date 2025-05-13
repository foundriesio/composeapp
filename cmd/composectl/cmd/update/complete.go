package updatectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

var (
	completeCmd = &cobra.Command{
		Use:   "complete",
		Short: "Complete the update process",
		Long: `Completes the update process by checking the status of 
the update and optionally uninstalling and removing apps that are not included into the update.`,
	}
)

type (
	completeOptions struct {
		Prune bool
	}
)

func init() {
	opts := completeOptions{}

	completeCmd.Flags().BoolVar(&opts.Prune, "prune", false,
		"Uninstall and remove the apps that are not included in the update.")
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

	var options []update.CompleteOpt
	if opts.Prune {
		options = append(options, update.CompleteWithPruning())
	}
	err = updateCtl.Complete(cmd.Context(), options...)
	ExitIfNotNil(err)
}
