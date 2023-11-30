package composectl

import (
	"fmt"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "ls",
	Short: "",
	Long:  ``,
	Args:  cobra.NoArgs,
	Run:   listApps,
}

func listApps(cmd *cobra.Command, args []string) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	apps, err := cs.ListApps(cmd.Context())
	DieNotNil(err)
	for _, a := range apps {
		fmt.Printf("%s -> %s\n", a.Name, a.String())
	}
}
