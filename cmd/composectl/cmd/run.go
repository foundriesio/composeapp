package composectl

import (
	"fmt"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"path"
	"strings"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "",
		Long:  ``,
		Args:  cobra.NoArgs,
		Run:   runApps,
	}
	runAppShortlist *string
)

func init() {
	rootCmd.AddCommand(runCmd)
	runAppShortlist = runCmd.Flags().String("apps", "", "Comma separated list of apps to run; all installed apps are started if not defined")
}

func runApps(cmd *cobra.Command, args []string) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	apps, err := cs.ListApps(cmd.Context())
	DieNotNil(err)
	var appShortlist map[string]bool
	if len(*runAppShortlist) > 0 {
		appShortlist = make(map[string]bool)
		for _, a := range strings.Split(*runAppShortlist, ",") {
			appShortlist[a] = true
		}
	}

	for _, a := range apps {
		if appShortlist != nil && !appShortlist[a.Name] {
			fmt.Printf("Skipping starting %s since it is not in the specfified shortlist\n", a.Name)
			continue
		}
		fmt.Printf("Starting %s --> %s\n", a.Name, a.Spec.String())
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = path.Join(config.ComposeRoot, a.Name)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		DieNotNil(cmd.Run())
	}
}
