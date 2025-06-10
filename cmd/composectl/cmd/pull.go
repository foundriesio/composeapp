package composectl

import (
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/moby/term"
	"github.com/spf13/cobra"
	"os"
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

		var currentBlob *compose.BlobInfo
		var lastSizeStr string
		var lastFetchBytes int64
		isTTY := term.IsTerminal(os.Stdout.Fd())

		err := compose.FetchBlobs(cmd.Context(), config, cr.MissingBlobs,
			compose.WithFetchProgress(func(p *compose.FetchProgress) {
				// Currently, we only support downloading one blob at a time,
				// so we assume that `p.Blobs` contains only one blob at the Fetching state.
				if currentBlob != nil {
					if lastFetchBytes == currentBlob.Fetched {
						// skip progress update if the fetched bytes haven't changed
						return
					}
					sizeStr := fmt.Sprintf("%.2f%% (%d)", (float64(currentBlob.Fetched)/float64(currentBlob.Descriptor.Size))*100, currentBlob.Fetched)
					if isTTY {
						if len(lastSizeStr) > 0 {
							// Move cursor back to overwrite previous percentage
							fmt.Printf("\x1b[%dD", len(lastSizeStr))
						}
						fmt.Print(sizeStr)
					} else {
						// Print progress update as a new line in log mode
						fmt.Printf(" %s", sizeStr)
					}
					lastSizeStr = sizeStr
				}

				// Find the blob that is currently being fetched
				var blobBeingFetched *compose.BlobInfo
				for _, bi := range p.Blobs {
					if bi.State == compose.BlobFetching {
						blobBeingFetched = bi
						break
					}
				}

				if blobBeingFetched == nil {
					return // No blob is currently being fetched
				}

				// If this is the first blob or the next blob then print the new blob info in the new line
				if currentBlob == nil || currentBlob.Descriptor.Digest != blobBeingFetched.Descriptor.Digest {
					currentBlob = blobBeingFetched
					fmt.Printf("\n [%-15s] %s %15d...",
						blobBeingFetched.Type,
						blobBeingFetched.Descriptor.Digest.Encoded(),
						blobBeingFetched.Descriptor.Size)
					lastSizeStr = ""
				} else {
					lastFetchBytes = blobBeingFetched.Fetched
				}
			}))
		DieNotNil(err, "failed to fetch blobs")
		fmt.Println("")
	}

	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app, platforms.OnlyStrict(config.Platform))
		DieNotNil(err)
	}
}
