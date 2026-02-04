package composectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/spf13/cobra"
)

type (
	rmOptions struct {
		Prune bool
		Quiet bool
	}
)

func init() {
	rmCmd := &cobra.Command{
		Use:   "rm",
		Short: "rm <app-name> | <ref> [<app-name> | <ref>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
	}
	opts := rmOptions{}
	rmCmd.Flags().BoolVar(&opts.Prune, "prune", true, "prune unused blobs after removing apps")
	rmCmd.Flags().BoolVar(&opts.Quiet, "quiet", false, "ignore non-existing apps")
	rmCmd.Run = func(cmd *cobra.Command, args []string) {
		rmApps(cmd, args, &opts)
	}
	rootCmd.AddCommand(rmCmd)
}

func rmApps(cmd *cobra.Command, args []string, opts *rmOptions) {
	appURIs := checkUserListedApps(cmd.Context(), config, args, !opts.Quiet)
	DieNotNil(compose.RemoveApps(cmd.Context(), config, appURIs, compose.WithBlobPruning(opts.Prune), compose.WithCheckStatus(!opts.Quiet)))
}
