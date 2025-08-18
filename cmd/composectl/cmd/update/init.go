package updatectl

import (
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	initOptions struct {
		UpdateRef         string
		AllowEmptyAppList bool // Allow empty app list to initialize the new update, which means update to the "no apps" state, hence removing all current apps.
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
	initCmd.Flags().BoolVarP(&opts.AllowEmptyAppList, "allow-empty-app-list", "r", false,
		"Initialize the update to the \"no apps\" state")

	initCmd.Run = func(cmd *cobra.Command, args []string) {
		initUpdateCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(initCmd)
}

func initUpdateCmd(cmd *cobra.Command, args []string, opts *initOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	var updateCtl update.Runner
	var renderProgress bool

	if len(args) > 0 || opts.AllowEmptyAppList {
		updateCtl, err = update.NewUpdate(cfg, opts.UpdateRef)
	} else {
		updateCtl, err = update.GetCurrentUpdate(cfg)
	}
	ExitIfNotNil(err)

	if len(args) > 0 {
		renderProgress = true
	} else if len(updateCtl.Status().URIs) > 0 {
		renderProgress = true
	}

	initOpts := []update.InitOption{
		update.WithInitAllowEmptyAppList(opts.AllowEmptyAppList),
		update.WithInitCheckStatus(true),
	}
	if renderProgress {
		initOpts = append(initOpts, update.WithInitProgress(update.GetInitProgressPrinter()))
	}

	err = updateCtl.Init(cmd.Context(), args, initOpts...)
	ExitIfNotNil(err)
	us := updateCtl.Status()
	fmt.Printf("Diff summary:\t\t\t\t  %d blobs (%s) to fetch\n", len(us.Blobs), compose.FormatBytesInt64(us.TotalBlobsBytes))
}
