package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"strings"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "run <app name> [<app name>] | --apps <app list>; if empty (\"\") then run all apps",
		Long:  ``,
		Args:  cobra.ArbitraryArgs,
	}
)

type (
	runOptions struct {
		Apps map[string]bool
	}
)

func init() {
	opts := runOptions{Apps: map[string]bool{}}
	runAppShortlist := runCmd.Flags().String("apps", ",", "Comma separated list of apps to run;"+
		" all installed apps are started if not defined")

	runCmd.Run = func(cmd *cobra.Command, args []string) {
		if *runAppShortlist == "," && len(args) == 0 {
			DieNotNil(fmt.Errorf("at least one app must be specified as an argument or in `--apps` parameter"))
		}
		if len(*runAppShortlist) > 0 && *runAppShortlist != "," {
			for _, a := range strings.Split(*runAppShortlist, ",") {
				opts.Apps[a] = true
			}
		} else {
			for _, a := range args {
				opts.Apps[a] = true
			}
		}

		runApps(cmd, &opts)
	}
	rootCmd.AddCommand(runCmd)
}

func runApps(cmd *cobra.Command, opts *runOptions) {
	cs, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	apps, err := cs.ListApps(cmd.Context())
	DieNotNil(err)

	foundApps := map[string]bool{}
	for _, app := range apps {
		foundApps[app.Name] = true
	}
	for app := range opts.Apps {
		if !foundApps[app] {
			DieNotNil(fmt.Errorf("specified app is not present in the local store: %s", app))
		}
	}

	checkedApps := map[string]compose.App{}
	for _, app := range apps {
		if len(opts.Apps) > 0 && !opts.Apps[app.Name] {
			fmt.Printf("%s: skipping, not in the shortlist\n", app.Name)
			continue
		}
		a, err := v1.NewAppLoader().LoadAppTree(cmd.Context(), cs, platforms.OnlyStrict(config.Platform), app.String())
		DieNotNil(err)
		if _, ok := checkedApps[app.Name]; ok {
			DieNotNil(fmt.Errorf("cannot start %s since there are two or more versions of it found in the store", app.Name))
		}
		checkedApps[app.Name] = a
	}

	for _, app := range checkedApps {
		fmt.Printf("Installing %s --> %s\n", app.Name(), app.Ref().String())
		err = v1.InstallApp(cmd.Context(), app, cs, config.GetBlobsRoot(), config.ComposeRoot, config.DockerHost)
		DieNotNil(err)
		fmt.Printf("Starting %s --> %s\n", app.Name(), app.Ref().String())
		cmd := exec.Command("docker", "compose", "up", "-d", "--remove-orphans")
		cmd.Dir = config.GetAppComposeDir(app.Name())
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		DieNotNil(cmd.Run())
	}
}
