package updatectl

import (
	"errors"
	"fmt"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

type (
	statusOptions struct {
		CheckApps bool
	}
)

func init() {
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Output the current or the last update status",
		Long:  `Output the current or the last update status`,
	}

	opts := statusOptions{}

	statusCmd.Flags().BoolVar(&opts.CheckApps, "check", false,
		"Check update apps' current status")
	statusCmd.Run = func(cmd *cobra.Command, args []string) {
		updateStatusCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(statusCmd)
}

func updateStatusCmd(cmd *cobra.Command, args []string, opts *statusOptions) {
	cfg, err := v1.NewDefaultConfig()
	ExitIfNotNil(err)

	var u *update.Update
	if updateCtl, err := update.GetCurrentUpdate(cfg); err == nil {
		curUpdate := updateCtl.Status()
		u = &curUpdate
	} else {
		if errors.Is(err, update.ErrUpdateNotFound) {
			u, err = update.GetFinalizedUpdate(cfg)
		}
		ExitIfNotNil(err)
	}

	// TODO: Implement update state output, receiver for update state
	cmd.Printf("ID: \t\t%s\n", u.ID)
	if u.ClientRef != "" {
		cmd.Printf("Client Ref: \t%s\n", u.ClientRef)
	}
	cmd.Printf("Date: \t\t%s\n", u.CreationTime.String())
	cmd.Printf("State: \t\t%s\n", u.State)
	cmd.Printf("Progress: \t%d\n", u.Progress)
	cmd.Printf("Download Size: \t%d\n", u.TotalBlobDownloadSize)
	cmd.Printf("Blobs: \t\t%d\n", len(u.Blobs))
	cmd.Println("URIs:")
	for _, appURI := range u.URIs {
		cmd.Printf("\t\t%s\n", appURI)
	}

	if opts.CheckApps {
		appsStatus, err := compose.CheckAppsStatus(cmd.Context(), cfg, u.URIs)
		ExitIfNotNil(err)

		fmt.Println()
		yesno := map[bool]string{false: "no", true: "yes"}
		fmt.Printf("Fetched: \t%s\n", yesno[appsStatus.AreFetched()])
		fmt.Printf("Installed: \t%s\n", yesno[appsStatus.AreInstalled()])
		fmt.Printf("Running: \t%s\n", yesno[appsStatus.AreRunning()])
	}
}
