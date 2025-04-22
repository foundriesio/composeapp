package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	dockercfg "github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/credentials"
	"github.com/spf13/cobra"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"
)

const (
	EnvOverrideDockerConfigDir = "DOCKER_CONFIG"
)

var (
	commit            string
	baseSystemConfig  string
	overrideConfigDir string
	storeRoot         string
	composeRoot       string
	arch              string
	dockerHost        string
	connectTimeout    int
	defConnectTimeout string
	showConfigFile    bool

	rootCmd = &cobra.Command{
		Use:   "composectl",
		Short: "Manage Compose Apps",
	}
	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "print a version of the utility",
		Long:  ``,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if len(commit) > 0 {
				fmt.Println(commit)
			} else {
				fmt.Println("unknown")
			}
		},
	}
)

func GetConfig() Config {
	return config
}

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
	var err error
	defConnectTimeoutValue := 30 // The default TCP connection timeout in seconds
	if len(defConnectTimeout) > 0 {
		fmt.Println(defConnectTimeout)
		defConnectTimeoutValue, err = strconv.Atoi(defConnectTimeout)
		DieNotNil(err)
	}

	rootCmd.PersistentFlags().StringVarP(&config.StoreRoot, "store", "s", storeRoot, "store root path")
	rootCmd.PersistentFlags().StringVarP(&config.ComposeRoot, "compose", "i", composeRoot, "compose projects root path")
	rootCmd.PersistentFlags().StringVarP(&arch, "arch", "a", "", "architecture of app/images to pull")
	rootCmd.PersistentFlags().StringVarP(&dockerHost, "host", "H", "", "path to the socket on which the Docker daemon listens")
	rootCmd.PersistentFlags().IntVarP(&connectTimeout, "connect-timeout", "", defConnectTimeoutValue,
		"timeout in seconds for establishing a connection to a container registry and an authentication service")
	rootCmd.PersistentFlags().BoolVarP(&showConfigFile, "show-config", "C", false, "print paths of the applied config files")
	rootCmd.AddCommand(versionCmd)
}

func initConfig() {
	var err error
	var cfg *configfile.ConfigFile

	if len(baseSystemConfig) > 0 {
		// Load the base system config if is defined
		cfgFile := filepath.Join(baseSystemConfig, dockercfg.ConfigFileName)
		if _, errExists := os.Stat(cfgFile); os.IsNotExist(errExists) {
			fmt.Printf("WARNING: the defined base system config is not found: %s; check configuration\n", cfgFile)
		} else {
			cfg, err = dockercfg.Load(baseSystemConfig)
			if err != nil {
				DieNotNil(err)
			}
			if showConfigFile {
				fmt.Printf("Applied config file: %s\n", cfgFile)
			}
		}
	}
	if len(overrideConfigDir) > 0 && len(os.Getenv(EnvOverrideDockerConfigDir)) == 0 {
		// If the default user config dir is overridden, then set the overridden one as a default,
		// unless `DOCKER_CONFIG` env var is set
		dockercfg.SetDir(overrideConfigDir)
	}
	cfgFile := filepath.Join(dockercfg.Dir(), dockercfg.ConfigFileName)
	if cfg == nil {
		cfg = configfile.New(cfgFile)
	}
	f, errOpen := os.Open(cfgFile)
	if errOpen != nil {
		if os.IsNotExist(errOpen) {
			if len(overrideConfigDir) > 0 || len(os.Getenv(EnvOverrideDockerConfigDir)) > 0 {
				fmt.Printf("WARNING: the specified config is not found: %s; check configuration\n", cfgFile)
			}
		} else {
			DieNotNil(errOpen)
		}
	} else {
		defer f.Close()
		err = cfg.LoadFromReader(f)
		DieNotNil(err)
		if showConfigFile {
			fmt.Printf("Applied config file: %s\n", cfgFile)
		}
	}
	if cfg != nil && !cfg.ContainsAuth() {
		cfg.CredentialsStore = credentials.DetectDefaultStore(cfg.CredentialsStore)
	}

	config.DockerCfg = cfg
	config.DockerHost = dockerHost
	config.Platform = platforms.DefaultSpec()
	if len(arch) > 0 {
		config.Platform.Architecture = arch
	}
	config.ConnectTimeout = time.Duration(connectTimeout) * time.Second
}
