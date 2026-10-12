package composectl

import (
	"encoding/json"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

type (
	pruneOptions struct {
		Format string
	}
)

func init() {
	pruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "prune dangling blobs",
		Long:  ``,
		Args:  cobra.NoArgs,
	}
	opts := pruneOptions{}
	pruneCmd.Flags().StringVar(&opts.Format, "format", "plain", "format the output. Values: [plain | json]")
	pruneCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "plain" && opts.Format != "json" {
			DieNotNil(fmt.Errorf("invalid value of `--format` option: %s", opts.Format))
		}
		pruneApps(cmd, &opts)
	}
	rootCmd.AddCommand(pruneCmd)
}

func pruneApps(cmd *cobra.Command, opts *pruneOptions) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	unlock, err := compose.LockInstall(config.StoreRoot)
	DieNotNil(err)
	defer unlock()
	prunedBlobs, err := cs.Prune(cmd.Context())
	DieNotNil(err)
	// Leases left by interrupted installs hold blobs and snapshots in dockerd's containerd.
	DieNotNil(compose.PruneInstallLeases(cmd.Context(), config), "failed to prune install leases")
	if opts.Format == "json" {
		if b, err := json.MarshalIndent(prunedBlobs, "", "  "); err == nil {
			fmt.Println(string(b))
		} else {
			DieNotNil(err)
		}
	} else {
		for _, b := range prunedBlobs {
			fmt.Println(b)
		}
	}
}
