package composectl

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/containerd/containerd/platforms"
	"github.com/docker/go-units"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/moby/term"
	"github.com/spf13/cobra"
)

type (
	pullOptions struct {
		UsageWatermark  uint
		ReservedStorage string
		SrcStorePath    string
		PrintUsageStat  bool
		Quick           bool
		Workers         uint
	}
)

const (
	MinWorkers     = 1
	MaxWorkers     = 10
	DefaultWorkers = 3
)

var (
	exitCodeInsufficientSpace int = 100
)

func init() {
	pullCmd := &cobra.Command{
		Use:   "pull <ref> [<ref>]",
		Short: "pull <ref> [<ref>]",
		Long:  ``,
		Args:  cobra.MinimumNArgs(1),
	}
	opts := pullOptions{}

	pullCmd.Flags().UintVarP(&opts.UsageWatermark, "storage-usage-watermark", "u", DefaultUsageWatermark,
		fmt.Sprintf("The maximum allowed storage usage in percentage in range %d-%d", MinUsageWatermark, MaxUsageWatermark))
	pullCmd.Flags().StringVar(&opts.ReservedStorage, "reserved-storage", "",
		"Absolute amount of free space to keep reserved, e.g. \"2GiB\" or \"500MB\"; takes precedence over --storage-usage-watermark")
	pullCmd.Flags().StringVarP(&opts.SrcStorePath, "source-store-path", "l", "", "A path to the source store root directory")
	pullCmd.Flags().BoolVarP(&opts.PrintUsageStat, "print-usage-stat", "p", false, "A flag to enable/disable usage statistic output to stderr")
	pullCmd.Flags().BoolVar(&opts.Quick, "quick", false, "Skip checking hash of app blobs; verify only their presence and size")
	pullCmd.Flags().UintVarP(&opts.Workers, "workers", "w", DefaultWorkers,
		fmt.Sprintf("Number of concurrent blob download workers in range %d-%d", MinWorkers, MaxWorkers))
	pullCmd.Run = func(cmd *cobra.Command, args []string) {
		watermark, watermarkInBytes := resolveWatermark(cmd, opts.UsageWatermark, opts.ReservedStorage)
		checkWorkers(opts.Workers)
		pullApps(cmd, args, &opts, watermark, watermarkInBytes)
	}

	rootCmd.AddCommand(pullCmd)
}

func checkWorkers(workers uint) {
	if workers < MinWorkers || workers > MaxWorkers {
		DieNotNilWithCode(fmt.Errorf("invalid `--workers` value: %d; should be between %d and %d",
			workers, MinWorkers, MaxWorkers), 1, "invalid argument")
	}
}

// resolveWatermark turns the storage-usage-watermark percentage and the optional
// reserved-storage size into the (watermark, inBytes) pair GetUsageInfo expects.
// reserved-storage takes precedence: when it is set the percentage watermark is
// ignored, with a warning if it was also explicitly provided.
func resolveWatermark(cmd *cobra.Command, usageWatermark uint, reservedStorage string) (watermark uint64, inBytes bool) {
	if len(reservedStorage) == 0 {
		checkWatermark(usageWatermark)
		return uint64(usageWatermark), false
	}
	if cmd.Flags().Changed("storage-usage-watermark") {
		fmt.Fprintln(os.Stderr,
			"warning: both --storage-usage-watermark and --reserved-storage are set; ignoring --storage-usage-watermark")
	}
	reserved, err := parseReservedStorage(reservedStorage)
	if err != nil || reserved <= 0 {
		DieNotNilWithCode(fmt.Errorf("invalid `--reserved-storage` value: %q; expected a byte size such as \"2GiB\" or \"500MB\"",
			reservedStorage), 1, "invalid argument")
	}
	return uint64(reserved), true
}

// parseReservedStorage accepts both binary (e.g. "2GiB", "500MiB") and decimal
// (e.g. "2GB", "500MB") byte-size suffixes. The presence of an "ib" suffix
// selects the binary parser; otherwise the decimal parser is used.
func parseReservedStorage(s string) (int64, error) {
	if strings.Contains(strings.ToLower(s), "ib") {
		return units.RAMInBytes(s)
	}
	return units.FromHumanSize(s)
}

func pullApps(cmd *cobra.Command, args []string, opts *pullOptions, watermark uint64, watermarkInBytes bool) {
	if len(args) > 1 {
		fmt.Printf("Pulling %d apps to %s\n", len(args), config.StoreRoot)
	} else {
		fmt.Printf("Pulling %s to %s\n", args[0], config.StoreRoot)
	}

	srcBlobProvider, cs, err := getAppStoreAndDstBlobProvider(opts.SrcStorePath, false)
	DieNotNil(err)

	cr, ui, apps, err := checkApps(cmd.Context(), args, srcBlobProvider, watermark, watermarkInBytes,
		opts.SrcStorePath, false, opts.Quick)
	DieNotNil(err, "failed to check apps status")
	if len(cr.MissingBlobs) > 0 {
		ui.Print()
		if ui.Required > ui.Available {
			if opts.PrintUsageStat {
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
			compose.WithSourcePath(opts.SrcStorePath),
			compose.WithFetchWorkers(int(opts.Workers)))
		DieNotNil(err, "failed to fetch blobs")
		fmt.Println("\n\nApp blobs pull completed at " + time.Now().UTC().Format("15:04:05 02 Jan 2006"))
	}

	for _, app := range apps {
		err = v1.MakeAkliteHappy(cmd.Context(), cs, app, platforms.OnlyStrict(config.Platform))
		DieNotNil(err)
	}
}

func getFetchProgressHandler() func(progress *compose.FetchProgress) {
	isTty := term.IsTerminal(os.Stdout.Fd()) || os.Getenv("PARENT_HAS_TTY") == "1"
	return NewFetchProgressRenderer(os.Stdout, isTty).Render
}
