package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
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

	checkedApps := map[string]string{}
	for _, app := range apps {
		if appShortlist != nil && !appShortlist[app.Name] {
			fmt.Printf("%s: skipping, not in the shortlist\n", app.Name)
			continue
		}
		_, _, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), cs, platforms.OnlyStrict(config.Platform), app.String())
		DieNotNil(err)
		if _, ok := checkedApps[app.Name]; ok {
			DieNotNil(fmt.Errorf("cannot start %s since there are two or more versions of it found in the store", app.Name))
		}
		checkedApps[app.Name] = app.String()
	}

	for app, ref := range checkedApps {
		fmt.Printf("Starting %s --> %s\n", app, ref)
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = path.Join(config.ComposeRoot, app)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		DieNotNil(cmd.Run())
	}
}
