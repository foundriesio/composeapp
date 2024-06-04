package composectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"strings"
)

var rmCmd = &cobra.Command{
	Use:   "rm",
	Short: "rm <app-name> | <ref> [<app-name> | <ref>]",
	Long:  ``,
	Args:  cobra.MinimumNArgs(1),
}

type (
	rmOptions struct {
		Prune bool
		Quiet bool
	}
)

func init() {
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
		if strings.Contains(arg, "/") {
			ref, err := compose.ParseAppRef(arg)
			DieNotNil(err)
			if err := ref.Digest.Validate(); err != nil {
				DieNotNil(fmt.Errorf("invalid app reference: %s", err.Error()))
			}
		}
		foundApp := false
		for _, storeApp := range storeApps {
			if arg == storeApp.Name || arg == storeApp.String() {
				appsToRemove = append(appsToRemove, storeApp)
				foundApp = true
			}
		}
		if !foundApp && !opts.Quiet {
			DieNotNil(fmt.Errorf("cannot remove non existing app: %s", arg))
		}
	}
	DieNotNil(cs.RemoveApps(cmd.Context(), appsToRemove, opts.Prune))
}
