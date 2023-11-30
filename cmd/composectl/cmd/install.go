package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"path"
)

var (
	installCmd = &cobra.Command{
		Use:   "install <ref>",
		Short: "",
		Long:  ``,
		Args:  cobra.ExactArgs(1),
		Run:   installApp,
	}
	composeInstallRootFlag string
	dockerHostFlag         string
)

func init() {
	rootCmd.AddCommand(installCmd)
	installCmd.Flags().StringVarP(&composeInstallRootFlag, "compose-dir", "p", "", "Compose projects root dir")
	installCmd.Flags().StringVarP(&dockerHostFlag, "docker-host", "d", "", "Docker daemon socket")
}

func installApp(cmd *cobra.Command, args []string) {
	composeRoot := config.ComposeRoot
	dockerHost := dockerHostFlag
	if len(composeInstallRootFlag) > 0 {
		composeRoot = composeInstallRootFlag
	}
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)

	appRef := args[0]
	fmt.Printf("Loading app metadata from the local store...")
	app, _, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), cs, platforms.OnlyStrict(config.Platform), appRef)
	DieNotNil(err)
	fmt.Println("ok")
	fmt.Printf("Extracting app compose archive to %s and loading its images to docker %s\n", composeRoot, dockerHost)
	err = v1.InstallApp(cmd.Context(), app, cs, path.Join(config.StoreRoot, "blobs/sha256"), composeRoot, dockerHost)
	DieNotNil(err)
}
