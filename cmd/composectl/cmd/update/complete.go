package updatectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	completeOptions struct {
		Prune bool
	}
)

func init() {
	completeCmd := &cobra.Command{
		Use:   "complete",
		Short: "Complete the update process",
		Long: `Complete the update process and optionally uninstall and remove apps not included in the update.

Run this command after the update has been installed and the updated apps are confirmed to be functioning correctly.
This will mark the update as successful after checking whether the update apps are fetched, installed, and running.`,
	}

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
