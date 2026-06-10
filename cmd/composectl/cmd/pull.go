package composectl

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/moby/term"
	"github.com/spf13/cobra"
)

type (
	pullOptions struct {
		UsageWatermark uint
		SrcStorePath   string
		PrintUsageStat bool
		Quick          bool
		Workers        uint
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
	pullCmd.Flags().StringVarP(&opts.SrcStorePath, "source-store-path", "l", "", "A path to the source store root directory")
	pullCmd.Flags().BoolVarP(&opts.PrintUsageStat, "print-usage-stat", "p", false, "A flag to enable/disable usage statistic output to stderr")
	pullCmd.Flags().BoolVar(&opts.Quick, "quick", false, "Skip checking hash of app blobs; verify only their presence and size")
	pullCmd.Flags().UintVarP(&opts.Workers, "workers", "w", DefaultWorkers,
		fmt.Sprintf("Number of concurrent blob download workers in range %d-%d", MinWorkers, MaxWorkers))
	pullCmd.Run = func(cmd *cobra.Command, args []string) {
		checkWatermark(opts.UsageWatermark)
		checkWorkers(opts.Workers)
		pullApps(cmd, args, &opts)
	}

	rootCmd.AddCommand(pullCmd)
}

func checkWorkers(workers uint) {
	if workers < MinWorkers || workers > MaxWorkers {
		DieNotNilWithCode(fmt.Errorf("invalid `--workers` value: %d; should be between %d and %d",
			workers, MinWorkers, MaxWorkers), 1, "invalid argument")
	}
}

func pullApps(cmd *cobra.Command, args []string, opts *pullOptions) {
	if len(args) > 1 {
		fmt.Printf("Pulling %d apps to %s\n", len(args), config.StoreRoot)
	} else {
		fmt.Printf("Pulling %s to %s\n", args[0], config.StoreRoot)
	}

	srcBlobProvider, cs, err := getAppStoreAndDstBlobProvider(opts.SrcStorePath, false)
	DieNotNil(err)

	cr, ui, apps, err := checkApps(cmd.Context(), args, srcBlobProvider, opts.UsageWatermark,
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
