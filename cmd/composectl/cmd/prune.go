package composectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "prune dangling blobs",
	Long:  ``,
	Args:  cobra.NoArgs,
	Run:   pruneApps,
}

func init() {
	rootCmd.AddCommand(pruneCmd)
}

func pruneApps(cmd *cobra.Command, args []string) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	DieNotNil(cs.Prune(cmd.Context()))
}
