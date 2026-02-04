package composectl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	dockercfg "github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/credentials"
	updatectl "github.com/foundriesio/composeapp/cmd/composectl/cmd/update"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
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
	readTimeout       int
	defConnectTimeout string
	showConfigFile    bool

	rootCmd = &cobra.Command{
		Use:   "composectl",
		Short: "Manage Compose Apps",
	}
	config *compose.Config
)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	versionCmd := &cobra.Command{
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
	cobra.OnInitialize(initConfig)
	// The `storeRoot`, `composeRoot`, `defConnectTimeout` can be set at compile time
	configOpts := []v1.ConfigOpt{
		v1.WithStoreRoot(storeRoot),
		v1.WithComposeRoot(composeRoot),
		v1.WithSkopeoSupport(true),
	}
	if len(defConnectTimeout) > 0 {
		defConnectTimeoutValue, err := strconv.Atoi(defConnectTimeout)
		DieNotNil(err)
		configOpts = append(configOpts, v1.WithConnectTimeout(time.Duration(defConnectTimeoutValue)*time.Second))
	}
	var err error
	config, err = v1.NewDefaultConfig(configOpts...)
	DieNotNil(err)

	rootCmd.PersistentFlags().StringVarP(&storeRoot, "store", "s", config.StoreRoot, "store root path")
	rootCmd.PersistentFlags().StringVarP(&composeRoot, "compose", "i", config.ComposeRoot, "compose projects root path")
	rootCmd.PersistentFlags().StringVarP(&arch, "arch", "a", "", "architecture of app/images to pull")
	rootCmd.PersistentFlags().StringVarP(&dockerHost, "host", "H", "", "path to the socket on which the Docker daemon listens")
	rootCmd.PersistentFlags().IntVarP(&connectTimeout, "connect-timeout", "", int(config.ConnectTimeout.Seconds()),
		"timeout in seconds for establishing a connection to a container registry and an authentication service")
	rootCmd.PersistentFlags().IntVarP(&readTimeout, "read-timeout", "", int(config.ReadTimeout.Seconds()),
		"timeout in seconds for reading data from a socket buffer when communicating with a container registry or an authentication service")
	rootCmd.PersistentFlags().BoolVarP(&showConfigFile, "show-config", "C", false, "print paths of the applied config files")
	rootCmd.AddCommand(updatectl.UpdateCmd)
	rootCmd.AddCommand(versionCmd)
}

func initConfig() {
	// get the docker config taking into account the overrides: baseSystemConfig and overrideConfigDir
	cfg, err := getDockerConfig()
	DieNotNil(err)

	// override the default config
	config.StoreRoot = storeRoot
	config.ComposeRoot = composeRoot
	config.ConnectTimeout = time.Duration(connectTimeout) * time.Second
	config.ReadTimeout = time.Duration(readTimeout) * time.Second
	config.DockerCfg = cfg
	config.DockerHost = dockerHost
	if len(arch) > 0 {
		config.Platform.Architecture = arch
	}
}

func getDockerConfig() (*configfile.ConfigFile, error) {
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
				return nil, err
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
			return nil, errOpen
		}
	} else {
		defer f.Close()
		err = cfg.LoadFromReader(f)
		if err != nil {
			return nil, err
		}
		if showConfigFile {
			fmt.Printf("Applied config file: %s\n", cfgFile)
		}
	}
	if cfg != nil && !cfg.ContainsAuth() {
		cfg.CredentialsStore = credentials.DetectDefaultStore(cfg.CredentialsStore)
	}
	return cfg, err
}

// Get the list of user listed apps as a command line arguments, specified  as names or URIs, and:
//  1. Check if the specified apps exist in the local app store.
//  2. Check if there is no ambiguity:
//     a) two or more versions of the same app are in the local store and the user specified the app name only.
//     b) app is referenced by different URIs or by name and URI.
//
// This guarantees that each app in the returned list is referenced unambiguously which is necessary for further processing for
// many app operations such as "run", "rm", etc.
// If `userListedApps` is empty, then return all apps from the local store.
// Return the list of validated app URIs.
func checkUserListedApps(ctx context.Context, cfg *compose.Config, userListedApps []string) []string {
	// Get the list of all apps in the local store
	apps, err := compose.ListApps(ctx, cfg)
	DieNotNil(err)

	inputAppRefs := userListedApps
	if len(inputAppRefs) == 0 {
		// Run all apps found in the local app store
		for _, app := range apps {
			inputAppRefs = append(inputAppRefs, app.Name())
		}
	}

	checkedApps := map[string]compose.App{}
	for _, appNameOrURI := range inputAppRefs {
		var foundName bool
		var foundURI bool
		var foundApp compose.App

		// Search for the app in the local app store by name or by URI
		for _, app := range apps {
			if app.Name() == appNameOrURI {
				if foundName {
					DieNotNil(fmt.Errorf("more than two versions of the same app found in the local app store:"+
						" %s (%s and %s)", app.Name(), foundApp.Ref().String(), app.Ref().String()))
				}
				foundName = true
				foundApp = app
				// Continue searching because there might be more than one version of the same app in the store
			} else if app.Ref().String() == appNameOrURI {
				foundURI = true
				foundApp = app
				// No need to continue searching because app URIs are unique
				break
			}
		}

		if !foundName && !foundURI {
			// App not found in the local app store
			DieNotNil(fmt.Errorf("app not found in local app store: %s", appNameOrURI))
		}

		if _, exists := checkedApps[foundApp.Name()]; exists {
			DieNotNil(fmt.Errorf("the same app specified more than once: %s", foundApp.Name()))
		} else {
			checkedApps[foundApp.Name()] = foundApp
		}
	}

	var appURIs []string
	for _, app := range checkedApps {
		appURIs = append(appURIs, app.Ref().String())
	}
	return appURIs
}
