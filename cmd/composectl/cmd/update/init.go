package updatectl

import (
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

type (
	initOptions struct {
		UpdateRef string
	}
)

func init() {
	initCmd := &cobra.Command{
		Use:   "init [app_ref]...",
		Short: "Initialize the update for specified apps by identifying required blobs to fetch",
		Long:  `Initialize or reinitialize an update for the specified apps by determining which blobs need to be downloaded to fetch the update`,
		Example: `
	# Initialize a new update for the specified apps:
	composectl update init <app1 URI> <app2 URI>...

	# Reinitialize an existing update:
	composectl update init`,
	}

	opts := initOptions{}

	initCmd.Flags().StringVar(&opts.UpdateRef, "ref", "",
		"Update reference/ID to associate the update with.")

	initCmd.Run = func(cmd *cobra.Command, args []string) {
		initUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(initCmd)
}

func initUpdateCmd(cmd *cobra.Command, args []string, opts *initOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	var bar *progressbar.ProgressBar
	var checkBlobProgress *progressbar.ProgressBar

	var updateCtl update.Runner
	if len(args) == 0 {
		updateCtl, err = update.GetCurrentUpdate(cfg)
		ExitIfNotNil(err)
		bar = progressbar.Default(int64(len(updateCtl.Status().URIs)))
	} else {
		updateCtl, err = update.NewUpdate(cfg, opts.UpdateRef)
		ExitIfNotNil(err)
		bar = progressbar.Default(int64(len(args)))
	}

	err = updateCtl.Init(cmd.Context(), args, update.WithInitProgress(func(status *update.InitProgress) {
		if status.State == update.UpdateInitStateLoadingTree {
			if err := bar.Set(status.Current); err != nil {
				cmd.Printf("Error setting progress bar: %s\n", err.Error())
			}
		} else {
			if checkBlobProgress == nil {
				checkBlobProgress = progressbar.Default(int64(status.Total))
			}
			if err := checkBlobProgress.Set(status.Current); err != nil {
				cmd.Printf("Error setting progress bar: %s\n", err.Error())
			}
		}
	}))
	ExitIfNotNil(err)
}
