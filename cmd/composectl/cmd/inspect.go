package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

func init() {
	inspectCmd := &cobra.Command{
		Use:   "inspect <app ref>",
		Short: "inspect <ref>",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
		Run:   inspectApp,
	}
	rootCmd.AddCommand(inspectCmd)
}

func inspectApp(cmd *cobra.Command, args []string) {
	appRef := args[0]

	fmt.Printf("Inspecting App %s...", appRef)
	app, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), compose.NewRemoteBlobProviderFromConfig(config), platforms.All, appRef)
	DieNotNil(err)
	fmt.Println("ok")
	app.Tree().Print()
	fmt.Printf("App tree node count: %d\n", app.NodeCount())
}
