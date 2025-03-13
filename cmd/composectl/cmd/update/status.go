package updatectl

import (
	"errors"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	"github.com/spf13/cobra"
)

var (
	statusCmd = &cobra.Command{
		Use:   "status",
		Short: "Output the current or the last update status",
		Long:  `Output the current or the last update status`,
	}
)

type (
	statusOptions struct {
	}
)

func init() {
	opts := statusOptions{}

	statusCmd.Run = func(cmd *cobra.Command, args []string) {
		updateStatusCmd(cmd, args, &opts)
	}

	UpdateCmd.AddCommand(statusCmd)
}

func updateStatusCmd(cmd *cobra.Command, args []string, opts *statusOptions) {
	cfg, err := compose.NewDefaultConfig()
	ExitIfNotNil(err)

	updateCtl, err := update.GetCurrentUpdate(cfg)
	if errors.Is(err, update.ErrUpdateNotFound) {
		updateCtl, err = update.GetLastUpdate(cfg)
	}
	ExitIfNotNil(err)

	u := updateCtl.Info()

	// TODO: Implement update state output, receiver for update state
	cmd.Printf("ID: \t\t%s\n", u.ID)
	if u.ClientRef != "" {
		cmd.Printf("Client Ref: \t%s\n", u.ClientRef)
	}
	cmd.Printf("Date: \t\t%s\n", u.Timestamp.String())
	cmd.Printf("State: \t\t%s\n", u.State)
	cmd.Printf("Progress: \t%d\n", u.Progress)
	cmd.Printf("Download Size: \t%d\n", u.TotalBlobDownloadSize)
	cmd.Printf("Blobs: \t\t%d\n", len(u.Blobs))
	cmd.Println("URIs:")
	for _, appURI := range u.URIs {
		cmd.Printf("\t\t%s\n", appURI)
	}
}
