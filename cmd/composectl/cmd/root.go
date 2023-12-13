package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	dockercfg "github.com/docker/cli/cli/config"
	"github.com/spf13/cobra"
	"os"
	"path"
)

const (
	EnvOverrideDockerConfigDir = "DOCKER_CONFIG"
)

var (
	overrideConfigDir string
	storeRoot         string
	composeRoot       string
	arch              string
	dockerHost        string

	rootCmd = &cobra.Command{
		Use:   "composectl",
		Short: "Manage Compose Apps",
	}
)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	if len(storeRoot) == 0 {
		homeDir, homeDirErr := os.UserHomeDir()
		if homeDirErr != nil {
			fmt.Printf("Failed to get a path to home directory, using the current directory as a store root directory\n")
			homeDir, homeDirErr = os.Getwd()
			DieNotNil(homeDirErr)
		}
		storeRoot = path.Join(homeDir, ".composeapps/store")
	}
	if len(composeRoot) == 0 {
		homeDir, homeDirErr := os.UserHomeDir()
		if homeDirErr != nil {
			fmt.Printf("Failed to get a path to home directory," +
				" using the current directory as a store root directory\n")
			homeDir, homeDirErr = os.Getwd()
			DieNotNil(homeDirErr)
		}
		composeRoot = path.Join(homeDir, ".composeapps/projects")
	}

	rootCmd.PersistentFlags().StringVarP(&config.StoreRoot, "store", "s", storeRoot, "store root path")
	rootCmd.PersistentFlags().StringVarP(&config.ComposeRoot, "compose", "i", composeRoot, "compose projects root path")
	rootCmd.PersistentFlags().StringVarP(&arch, "arch", "a", "", "architecture of app/images to pull")
	rootCmd.PersistentFlags().StringVarP(&dockerHost, "host", "H", "", "path to the socket on which the Docker daemon listens")
}

func initConfig() {
	if len(overrideConfigDir) > 0 && len(os.Getenv(EnvOverrideDockerConfigDir)) == 0 {
		dockercfg.SetDir(overrideConfigDir)
	}
	cfg := dockercfg.LoadDefaultConfigFile(os.Stderr)
	if cfg == nil {
		os.Exit(1)
	}
	config.DockerCfg = cfg
	config.DockerHost = dockerHost
	config.Platform = platforms.DefaultSpec()
	if len(arch) > 0 {
		config.Platform.Architecture = arch
	}
}
