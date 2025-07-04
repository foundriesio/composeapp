package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
)

func init() {
	installCmd := &cobra.Command{
		Use:   "install <ref>",
		Short: "install <ref>",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
		Run:   installApp,
	}
	rootCmd.AddCommand(installCmd)
}

func installApp(cmd *cobra.Command, args []string) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)

	appRef := args[0]
	fmt.Printf("Loading app metadata from the local store...")
	app, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), cs, platforms.OnlyStrict(config.Platform), appRef)
	DieNotNil(err)
	fmt.Println("ok")
	fmt.Printf("Extracting app compose archive to %s and loading its images to docker %s\n", composeRoot, dockerHost)
	err = v1.InstallApp(cmd.Context(), app, cs, config.GetBlobsRoot(), config.ComposeRoot, config.DockerHost)
	DieNotNil(err)
}
