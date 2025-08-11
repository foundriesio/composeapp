package updatectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
	"strings"
	"time"
)

type (
	fetchOptions struct {
	}
)

func init() {
	fetchCmd := &cobra.Command{
		Use:   "fetch",
		Short: "Download missing blobs for the apps being updated",
		Long:  `Fetch the update by downloading any missing blobs for the apps being updated.`,
	}

	opts := fetchOptions{}

	fetchCmd.Run = func(cmd *cobra.Command, args []string) {
		fetchUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(fetchCmd)
}

func fetchUpdateCmd(cmd *cobra.Command, args []string, opts *fetchOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	ExitIfNotNil(err)

	fetchOpts := []compose.FetchOption{
		compose.WithProgressPollInterval(500),
	}
	if len(updateCtl.Status().URIs) > 0 {
		fetchOpts = append(fetchOpts, compose.WithFetchProgress(getProgressHandler()))
	}
	ExitIfNotNil(updateCtl.Fetch(cmd.Context(), fetchOpts...))
}

func getProgressHandler() func(status *compose.FetchProgress) {
	var start time.Time
	var printed bool

	return func(status *compose.FetchProgress) {
		if printed {
			fmt.Print("\033[1A\033[J")
		}

		var blobsBeingFetched int
		var curSpeedTotal int64
		var avgSpeedTotal int64
		for _, b := range status.Blobs {
			if b.State == compose.BlobOk || b.FetchStartTime.IsZero() {
				// Blob is already fetched or has not being started yet
				continue
			}
			// Count blobs that are currently being fetched
			blobsBeingFetched++
			// Calculate the total current speed of all blobs being fetched
			curSpeedTotal += b.ReadSpeedCur
			// Calculate the total average speed of all blobs being fetched
			avgSpeedTotal += b.ReadSpeedAvg
			// Set the fetch start time to the start time of the first blob if not set
			if start.IsZero() || (!b.FetchStartTime.IsZero() && b.FetchStartTime.Before(start)) {
				start = b.FetchStartTime
			}
		}
		var curSpeed int64
		var avgSpeed int64
		if blobsBeingFetched > 0 {
			curSpeed = curSpeedTotal / int64(blobsBeingFetched)
			avgSpeed = avgSpeedTotal / int64(blobsBeingFetched)
		}

		// progress in percentage
		pct := float64(status.CurrentBytes) / float64(status.TotalBytes)

		// calculate elapsed time if fetch of at least one blob has started
		var elapsed time.Duration
		if !start.IsZero() {
			elapsed = time.Since(start).Round(time.Second)
		}
		var eta time.Duration
		if avgSpeed > 0 {
			eta = time.Duration((status.TotalBytes-status.CurrentBytes)/avgSpeed) * time.Second
		}

		// First line
		fmt.Printf("%4.0f%%  %s  %9s / %9s | %d/%d blobs | Cur: %9s/s | Avg: %9s/s | Time: %s | ETA: %s\n",
			pct*100,
			renderBar(pct, 25),
			compose.FormatBytesInt64(status.CurrentBytes),
			compose.FormatBytesInt64(status.TotalBytes),
			status.FetchedCount,
			len(status.Blobs),
			compose.FormatBytesInt64(int64(curSpeed)),
			compose.FormatBytesInt64(int64(avgSpeed)),
			elapsed.Round(time.Second),
			eta.Round(time.Second),
		)

		printed = true
	}
}

func renderBar(pct float64, width int) string {
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", width-filled) + "]"
}
