package composectl

import (
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"os"
	"strings"
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

	cr, ui, apps, err := checkApps(cmd.Context(), args, srcBlobProvider, *pullUsageWatermark,
		*pullSrcStorePath, false, true)
	DieNotNil(err, "failed to check apps status")
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
		//// copying missing blobs
		//// TODO:  move to a separate function:
		////  1) Copy in multiple goroutines/workers (configurable)
		////  2) Generic status reporting mechanism
		//authorizer := compose.NewRegistryAuthorizer(config.DockerCfg, config.ConnectTimeout)
		//resolver := compose.NewResolver(authorizer, config.ConnectTimeout)
		//
		//ls, err := local.NewStore(config.StoreRoot)
		//DieNotNil(err)
		//for _, b := range cr.MissingBlobs {
		//	fmt.Printf(" [%-15s] %s %15d ... ", b.Type, b.Descriptor.Digest.Encoded(), b.Descriptor.Size)
		//	var copyErr error
		//	if len(*pullSrcStorePath) > 0 {
		//		blobPath := path.Join(compose.GetBlobsRootFor(*pullSrcStorePath), b.Descriptor.Digest.Encoded())
		//		copyErr = compose.CopyLocalBlob(cmd.Context(), blobPath, b.Descriptor.URLs[0], *b.Descriptor, ls, true)
		//	} else {
		//		copyErr = compose.CopyBlob(cmd.Context(), resolver, b.Descriptor.URLs[0], *b.Descriptor, ls, true)
		//	}
		//	DieNotNil(copyErr)
		//	fmt.Println("ok")
		//}

		var printedLines = 0

		err := compose.FetchBlobs(cmd.Context(), config, cr.MissingBlobs, compose.WithFetchProgress(func(progress *compose.FetchProgress) {
			// Move the cursor up to overwrite previous output
			if printedLines > 0 {
				fmt.Printf("\033[%dA", printedLines)
			}

			var sb strings.Builder

			// Line 1: Overall progress
			sb.WriteString(fmt.Sprintf("Blobs downloaded: %d/%d\n", progress.Current, progress.Total))

			// Line 2: Current blob download (if any)
			if len(progress.Blobs) > 0 {
				blob := progress.Blobs[0]
				var percent float64
				if blob.Descriptor.Size > 0 {
					percent = float64(blob.Fetched) / float64(blob.Descriptor.Size) * 100
				}
				sb.WriteString(fmt.Sprintf("Downloading %s: %d/%d bytes (%.1f%%)\n",
					blob.Descriptor.Digest, blob.Fetched, blob.Descriptor.Size, percent))
			} else {
				sb.WriteString("Waiting for blob...\n")
			}

			// Print output and update printed line count
			output := sb.String()
			printedLines = strings.Count(output, "\n")
			fmt.Print(output)
		}))
		DieNotNil(err, "failed to fetch blobs")
	}

	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app, platforms.OnlyStrict(config.Platform))
		DieNotNil(err)
	}
}
