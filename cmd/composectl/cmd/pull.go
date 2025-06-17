package composectl

import (
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/go-units"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/moby/term"
	"github.com/spf13/cobra"
	"os"
	"time"
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
		fmt.Println("Pulling app blobs, starting at " + time.Now().UTC().Format("15:04:05 02 Jan 2006") + "...")

		err := compose.FetchBlobs(cmd.Context(), config, cr.MissingBlobs,
			compose.WithProgressPollInterval(1000),
			compose.WithFetchProgress(getHandleFetchProgress1()))
		DieNotNil(err, "failed to fetch blobs")
		fmt.Println("\n\nApp blobs pull completed at " + time.Now().UTC().Format("15:04:05 02 Jan 2006"))
	}

	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app, platforms.OnlyStrict(config.Platform))
		DieNotNil(err)
	}
}

func getHandleFetchProgress() func(progress *compose.FetchProgress) {
	var currentBlob *compose.BlobFetchProgress
	var lastSizeStr string
	var lastFetchBytes int64
	isTTY := term.IsTerminal(os.Stdout.Fd())

	return func(p *compose.FetchProgress) {
		var blobBeingFetched *compose.BlobFetchProgress
		for _, bi := range p.Blobs {
			if bi.State == compose.BlobFetching {
				blobBeingFetched = bi
				break
			}
		}

		if currentBlob != nil && currentBlob != blobBeingFetched {
			speed := units.HumanSize(float64(currentBlob.BytesFetched-currentBlob.BlobInfo.BytesFetched) / (time.Since(currentBlob.FetchStartTime).Seconds()))
			sizeStr := fmt.Sprintf("downloaded %8s at %s (%s/s avg)",
				units.HumanSize(float64(currentBlob.BytesFetched)),
				time.Now().UTC().Format("15:04:05"),
				speed)
			if isTTY {
				if len(lastSizeStr) > 0 {
					fmt.Printf("\x1b[%dD", len(lastSizeStr))
				}
				fmt.Print(sizeStr)
			} else {
				fmt.Printf("\n [%-15s] %s %15d ... ",
					currentBlob.Type,
					currentBlob.Descriptor.Digest.Encoded(),
					currentBlob.Descriptor.Size)
				fmt.Printf(" %s", sizeStr)
			}
		}

		if blobBeingFetched == nil {
			return // No blob is currently being fetched
		}

		if currentBlob != nil && lastFetchBytes == currentBlob.BytesFetched {
			// skip progress update if the fetched bytes haven't changed
			return
		}

		// If this is the first blob or the next blob then print the new blob info in the new line
		if currentBlob == nil || currentBlob.Descriptor.Digest != blobBeingFetched.Descriptor.Digest {
			if isTTY {
				fmt.Printf("\n [%-15s] %s %15d ... ",
					blobBeingFetched.Type,
					blobBeingFetched.Descriptor.Digest.Encoded(),
					blobBeingFetched.Descriptor.Size)
			}
			lastSizeStr = ""
			currentBlob = blobBeingFetched
		}

		speed := units.HumanSize(float64(currentBlob.BytesFetched-currentBlob.BlobInfo.BytesFetched) / (time.Since(currentBlob.FetchStartTime).Seconds()))
		var sizeStr string
		if currentBlob.BytesFetched == currentBlob.Descriptor.Size {
			sizeStr = fmt.Sprintf("downloaded %8s at %s (%s/s avg)",
				units.HumanSize(float64(currentBlob.BytesFetched)),
				time.Now().UTC().Format("15:04:05"),
				speed)
			currentBlob = nil // Reset currentBlob after completion
		} else {
			sizeStr = fmt.Sprintf("%8s / %8s; %d%%; %s/s",
				units.HumanSize(float64(currentBlob.BytesFetched)),
				units.HumanSize(float64(currentBlob.Descriptor.Size)),
				int((float64(currentBlob.BytesFetched)/float64(currentBlob.Descriptor.Size))*100),
				speed)
		}

		if isTTY {
			if len(lastSizeStr) > 0 {
				// Move cursor back to overwrite previous percentage
				fmt.Printf("\x1b[%dD", len(lastSizeStr))
			}
			fmt.Print(sizeStr)
		} else if currentBlob != nil {
			// Print progress update as a new line in log mode
			fmt.Printf("\n [%-15s] %s %15d ... ",
				currentBlob.Type,
				currentBlob.Descriptor.Digest.Encoded(),
				currentBlob.Descriptor.Size)
			fmt.Printf(" %s", sizeStr)
		}
		lastSizeStr = sizeStr
	}
}

func getHandleFetchProgress1() func(progress *compose.FetchProgress) {
	var blobsBeingFetched []struct {
		blob *compose.BlobFetchProgress
		done bool
	}
	return func(p *compose.FetchProgress) {
		currentBlobsBeingFetchedNumb := len(blobsBeingFetched)
		for _, bi := range p.Blobs {
			found := false
			for _, bf := range blobsBeingFetched {
				if bf.blob.Descriptor.Digest == bi.Descriptor.Digest {
					found = true
					break
				}
			}
			if !found && (bi.State == compose.BlobFetching || bi.State == compose.BlobOk) {
				blobsBeingFetched = append(blobsBeingFetched, struct {
					blob *compose.BlobFetchProgress
					done bool
				}{blob: bi, done: false})
			}
		}
		//fmt.Printf(">> Move %d back: ", currentBlobsBeingFetchedNumb)
		//time.Sleep(1* time.Second)
		fmt.Printf("\033[%dA\r", currentBlobsBeingFetchedNumb)

		for i := range blobsBeingFetched {
			if blobsBeingFetched[i].done {
				fmt.Print("\033[1B")
				continue
			}

			b := blobsBeingFetched[i].blob

			fmt.Printf("\n [%-15s] %s started at %s from %10s; ",
				b.Type,
				b.Descriptor.Digest.Encoded(),
				b.FetchStartTime.UTC().Format("15:04:05"),
				compose.FormatBytesInt64(b.BlobInfo.BytesFetched))

			fmt.Printf("%10s / %10s; %4d%%; %10s/s",
				compose.FormatBytesInt64(b.BytesFetched),
				compose.FormatBytesInt64(b.Descriptor.Size),
				int((float64(b.BytesFetched)/float64(b.Descriptor.Size))*100),
				compose.FormatBytesInt64(b.FetchSpeedAvg))

			if b.BytesFetched == b.Descriptor.Size {
				fmt.Printf("; done at %s",
					time.Now().UTC().Format("15:04:05"))
				blobsBeingFetched[i].done = true
			}
		}
	}
}
