package updatectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	completeOptions struct {
		Prune          bool
		PruneAllImages bool
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
		"Uninstall and remove the apps that are not included in the update and images referenced by those apps")
	completeCmd.Flags().BoolVar(&opts.PruneAllImages, "prune-all-images", false,
		"Remove all unused images, even those that are not associated with the apps being uninstalled and pruned by the update complete process."+
			" This option is only effective when --prune is also specified.")
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
		imagePruneType := compose.PruneTypeOnlyAppImages
		if opts.PruneAllImages {
			imagePruneType = compose.PruneTypeAllUnusedImages
		}
		options = append(options, update.CompleteWithPruning(imagePruneType))
	}
	err = updateCtl.Complete(cmd.Context(), options...)
	ExitIfNotNil(err)
}
