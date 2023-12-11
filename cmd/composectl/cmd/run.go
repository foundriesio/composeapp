package composectl

import (
	"fmt"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"path"
)

func init() {
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "",
	Long:  ``,
	Args:  cobra.NoArgs,
	Run:   runApps,
}

func runApps(cmd *cobra.Command, args []string) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	apps, err := cs.ListApps(cmd.Context())
	DieNotNil(err)
	for _, a := range apps {
		fmt.Printf("Starting %s --> %s\n", a.Name, a.Spec.String())
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = path.Join(config.ComposeRoot, a.Name)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		DieNotNil(cmd.Run())
	}
}
