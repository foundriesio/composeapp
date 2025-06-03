package composectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"strings"
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
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	storeApps, err := cs.ListApps(cmd.Context())
	DieNotNil(err)
	var appsToRemove []*compose.AppRef
	for _, arg := range args {
		foundApp := false
		if strings.Contains(arg, "/") {
			ref, err := compose.ParseAppRef(arg)
			DieNotNil(err)
			if err := ref.Digest.Validate(); err != nil {
				DieNotNil(fmt.Errorf("invalid app reference: %s", err.Error()))
			}
			// Check if the app manifest is present in the store's blobs directory,
			// if so, then consider it found even if it is missing in the store's apps directory
			if _, err := cs.Info(cmd.Context(), ref.Digest); err == nil {
				appsToRemove = append(appsToRemove, ref)
				foundApp = true
			}
		}
		if !foundApp {
			for _, storeApp := range storeApps {
				if arg == storeApp.Name || arg == storeApp.String() {
					appsToRemove = append(appsToRemove, storeApp)
					foundApp = true
				}
			}
		}
		if !foundApp && !opts.Quiet {
			DieNotNil(fmt.Errorf("cannot remove non existing app: %s", arg))
		}
	}
	DieNotNil(cs.RemoveApps(cmd.Context(), appsToRemove, opts.Prune))
}
