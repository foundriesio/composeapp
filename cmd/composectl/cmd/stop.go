package composectl

import (
	"fmt"
	"github.com/spf13/cobra"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

type (
	stopOptions struct {
		All bool
	}
)

func init() {
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "stop --all | <app-name> [<app-name>]",
		Long:  ``,
		Args:  cobra.ArbitraryArgs,
	}
	opts := stopOptions{}
	stopCmd.Flags().BoolVar(&opts.All, "all", false, "stop all installed and running apps")
	stopCmd.Run = func(cmd *cobra.Command, args []string) {
		stopApps(args, &opts)
	}

	rootCmd.AddCommand(stopCmd)
}

func stopApps(args []string, opts *stopOptions) {
	if len(args) > 0 && opts.All {
		DieNotNil(fmt.Errorf("`--all` flag cannot be specified if at least one app is specified as parameter"))
	}
	if len(args) == 0 && !opts.All {
		DieNotNil(fmt.Errorf("either `--all` flag or app name should be specified"))
	}

	appsToStop, err := getAllAppsToStop(config.ComposeRoot)
	DieNotNil(err)

	if len(args) > 0 && !opts.All {
		for _, a := range args {
			found := false
			for _, installedApp := range appsToStop {
				if a == installedApp {
					found = true
					break
				}
			}
			if !found {
				DieNotNil(fmt.Errorf("the specified app is not installed: %s", a))
			}
		}
		appsToStop = args
	}

	for _, app := range appsToStop {
		cmd := exec.Command("docker", "compose", "down")
		cmd.Dir = config.GetAppComposeDir(app)
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		DieNotNil(cmd.Run())
	}
}

func getAllAppsToStop(composeRoot string) ([]string, error) {
	var apps []string
	err := filepath.Walk(composeRoot, func(path string, info fs.FileInfo, err error) error {
		if !info.IsDir() || path == config.ComposeRoot {
			return nil
		}
		apps = append(apps, info.Name())
		return nil
	})
	return apps, err
}
