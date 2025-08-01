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
	"sync/atomic"
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
			compose.WithFetchProgress(getFetchProgressHandler()),
			compose.WithSourcePath(*pullSrcStorePath))
		DieNotNil(err, "failed to fetch blobs")
		fmt.Println("\n\nApp blobs pull completed at " + time.Now().UTC().Format("15:04:05 02 Jan 2006"))
	}

	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app, platforms.OnlyStrict(config.Platform))
		DieNotNil(err)
	}
}

func getFetchProgressHandler() func(progress *compose.FetchProgress) {
	var blobsBeingFetched []struct {
		blob *compose.BlobFetchProgress
		done bool
	}
	isTty := term.IsTerminal(os.Stdout.Fd()) || os.Getenv("PARENT_HAS_TTY") == "1"
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

		if isTty && currentBlobsBeingFetchedNumb > 0 {
			fmt.Printf("\033[%dA\r", currentBlobsBeingFetchedNumb)
		}

		for i := range blobsBeingFetched {
			if blobsBeingFetched[i].done {
				if isTty {
					// Move cursor down one line since this blob fetch is completed
					fmt.Print("\033[1B")
				}
				continue
			}

			b := blobsBeingFetched[i].blob

			fmt.Printf("\n [%-12s] %.12s start: %s from: %10s ",
				b.Type,
				b.Descriptor.Digest.Encoded(),
				b.FetchStartTime.UTC().Format(time.TimeOnly),
				compose.FormatBytesInt64(b.BlobInfo.BytesFetched))

			fmt.Printf("progress: %10s / %10s (%3.0f%%) avg: %10s/s cur: %10s/s",
				compose.FormatBytesInt64(atomic.LoadInt64(&b.BytesFetched)),
				compose.FormatBytesInt64(b.Descriptor.Size),
				100*float64(b.BytesFetched)/float64(b.Descriptor.Size),
				compose.FormatBytesInt64(atomic.LoadInt64(&b.ReadSpeedAvg)),
				compose.FormatBytesInt64(atomic.LoadInt64(&b.ReadSpeedCur)))

			if b.BytesFetched == b.Descriptor.Size {
				fmt.Printf("; done at %s",
					time.Now().UTC().Format(time.TimeOnly))
				blobsBeingFetched[i].done = true
			}
		}
	}
}
