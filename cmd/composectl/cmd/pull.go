package composectl

import (
	"fmt"
	"github.com/containerd/containerd/content/local"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"path"
)

var (
	pullCmd = &cobra.Command{
		Use:   "pull <ref> [<ref>]",
		Short: "",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
		Run:   pullApps,
	}
	pullUsageWatermark *uint
	pullSrcStorePath   *string
)

func init() {
	rootCmd.AddCommand(pullCmd)
	pullUsageWatermark = pullCmd.Flags().UintP("storage-usage-watermark", "u", 80, "The maximum allowed storage usage in percentage")
	pullSrcStorePath = pullCmd.Flags().StringP("source-store-path", "l", "", "A path to the source store root directory")
}

func pullApps(cmd *cobra.Command, args []string) {
	if len(args) > 1 {
		fmt.Printf("Pulling %d apps to %s\n", len(args), config.StoreRoot)
	} else {
		fmt.Printf("Pulling %s to %s\n", args[0], config.StoreRoot)
	}

	cr, ui, apps := checkApps(cmd.Context(), args, *pullUsageWatermark, *pullSrcStorePath)
	fmt.Printf("required: %d (%g%%), available: %d (%g%%) at %s, size: %d (100%%), free: %d (%g%%), reserved: %d (%g%%)\n",
		ui.Required, ui.RequiredP, ui.Available, ui.AvailableP, ui.Path, ui.SizeB, ui.Free, ui.FreeP, ui.Reserved, ui.ReservedP)

	if ui.Required > ui.Available {
		DieNotNil(fmt.Errorf("Not enough available storage"))
	}
	fmt.Printf("Pulling %d blobs; total download size: %d, total store size: %d, total runtime size of missing blobs: %d, total required %d...\n",
		len(cr.missingBlobs), cr.totalPullSize, cr.totalStoreSize, cr.totalRuntimeSize, cr.totalStoreSize+cr.totalRuntimeSize)

	// copying missing blobs
	// TODO:  move to a separate function:
	//  1) Copy in multiple goroutines/workers (configurable)
	//  2) Generic status reporting mechanism
	authorizer := compose.NewRegistryAuthorizer(config.DockerCfg)
	resolver := compose.NewResolver(authorizer)

	ls, err := local.NewStore(config.StoreRoot)
	DieNotNil(err)
	for _, b := range cr.missingBlobs {
		fmt.Printf(" [%-15s] %s %15d ... ", b.Type, b.Descriptor.Digest.Encoded(), b.Descriptor.Size)
		var copyErr error
		if len(*pullSrcStorePath) > 0 {
			blobPath := path.Join(*pullSrcStorePath, "blobs/sha256", b.Descriptor.Digest.Encoded())
			copyErr = compose.CopyLocalBlob(cmd.Context(), blobPath, b.Descriptor.URLs[0], *b.Descriptor, ls, true)
		} else {
			copyErr = compose.CopyBlob(cmd.Context(), resolver, b.Descriptor.URLs[0], *b.Descriptor, ls, true)
		}
		DieNotNil(copyErr)
		fmt.Println("ok")
	}

	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app)
		DieNotNil(err)
	}

}
