package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(inspectCmd)
}

var inspectCmd = &cobra.Command{
	Use:   "inspect <app ref>",
	Short: "",
	Long:  ``,
	Args:  cobra.ExactArgs(1),
	Run:   inspectApp,
}

func inspectApp(cmd *cobra.Command, args []string) {
	appRef := args[0]

	authorizer := compose.NewRegistryAuthorizer(config.DockerCfg)
	resolver := compose.NewResolver(authorizer, config.ConnectTime)
	fmt.Printf("Inspecting App %s...", appRef)
	_, tree, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), compose.NewRemoteBlobProvider(resolver), platforms.All, appRef)
	DieNotNil(err)
	fmt.Println("ok")
	tree.Print()
}
