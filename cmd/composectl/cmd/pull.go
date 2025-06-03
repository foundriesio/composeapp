package composectl

import (
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"os"
	"path"
)

var (
	pullUsageWatermark        *uint
	pullSrcStorePath          *string
	pullPrintUsageStat        *bool
	exitCodeInsufficientSpace int = 100
)

func init() {
	pullCmd := &cobra.Command{
		Use:   "pull <ref> [<ref>]",
		Short: "pull <ref> [<ref>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
		Run:   pullApps,
	}
	rootCmd.AddCommand(pullCmd)
	pullUsageWatermark = pullCmd.Flags().UintP("storage-usage-watermark", "u", 80, "The maximum allowed storage usage in percentage")
	pullSrcStorePath = pullCmd.Flags().StringP("source-store-path", "l", "", "A path to the source store root directory")
	pullPrintUsageStat = pullCmd.Flags().BoolP("print-usage-stat", "p", false, "A flag to enable/disable usage statistic output to stderr")
}

func pullApps(cmd *cobra.Command, args []string) {
	if len(args) > 1 {
		fmt.Printf("Pulling %d apps to %s\n", len(args), config.StoreRoot)
	} else {
		fmt.Printf("Pulling %s to %s\n", args[0], config.StoreRoot)
	}

	srcBlobProvider, cs, err := getAppStoreAndDstBlobProvider(*pullSrcStorePath, false)
	DieNotNil(err)

	cr, ui, apps := checkApps(cmd.Context(), args, cs, srcBlobProvider, *pullUsageWatermark,
		*pullSrcStorePath, false, true)
	if len(cr.MissingBlobs) > 0 {
		ui.Print()
		if ui.Required > ui.Available {
			if *pullPrintUsageStat {
				if b, err := json.Marshal(ui); err == nil {
					fmt.Fprintln(os.Stderr, string(b))
				}
			}
			DieNotNilWithCode(fmt.Errorf("not enough storage available"), exitCodeInsufficientSpace)
		}
		cr.print()
		fmt.Println("Pulling app blobs...")
		// copying missing blobs
		// TODO:  move to a separate function:
		//  1) Copy in multiple goroutines/workers (configurable)
		//  2) Generic status reporting mechanism
		authorizer := compose.NewRegistryAuthorizer(config.DockerCfg, config.ConnectTimeout)
		resolver := compose.NewResolver(authorizer, config.ConnectTimeout)

		ls, err := local.NewStore(config.StoreRoot)
		DieNotNil(err)
		for _, b := range cr.MissingBlobs {
			fmt.Printf(" [%-15s] %s %15d ... ", b.Type, b.Descriptor.Digest.Encoded(), b.Descriptor.Size)
			var copyErr error
			if len(*pullSrcStorePath) > 0 {
				blobPath := path.Join(compose.GetBlobsRootFor(*pullSrcStorePath), b.Descriptor.Digest.Encoded())
				copyErr = compose.CopyLocalBlob(cmd.Context(), blobPath, b.Descriptor.URLs[0], *b.Descriptor, ls, true)
			} else {
				copyErr = compose.CopyBlob(cmd.Context(), resolver, b.Descriptor.URLs[0], *b.Descriptor, ls, true)
			}
			DieNotNil(copyErr)
			fmt.Println("ok")
		}
	}

	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app, platforms.OnlyStrict(config.Platform))
		DieNotNil(err)
	}
}
