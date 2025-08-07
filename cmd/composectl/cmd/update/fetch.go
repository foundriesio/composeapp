package updatectl

import (
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
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
		bar := progressbar.DefaultBytes(updateCtl.Status().TotalBlobsBytes)
		fetchOpts = append(fetchOpts, compose.WithFetchProgress(func(status *compose.FetchProgress) {
			if err := bar.Set64(status.CurrentBytes); err != nil {
				cmd.Printf("Error setting progress bar: %s\n", err.Error())
			}
		}))
	}
	ExitIfNotNil(updateCtl.Fetch(cmd.Context(), fetchOpts...))

}
