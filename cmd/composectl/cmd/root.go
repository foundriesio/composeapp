package composectl

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	dockercfg "github.com/docker/cli/cli/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
	"path"
)

var (
	cfgFile     string
	storeRoot   string
	composeRoot string
	arch        string

	rootCmd = &cobra.Command{
		Use:   "composectl",
		Short: "Manage Compose Apps",
		Long:  `TODO`,
		Run: func(cmd *cobra.Command, args []string) {
			// Do Stuff Here
		},
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
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file")
	rootCmd.PersistentFlags().StringVarP(&storeRoot, "store", "s", "", "store root path")
	rootCmd.PersistentFlags().StringVarP(&composeRoot, "compose", "i", "", "compose projects root path")
	rootCmd.PersistentFlags().StringVarP(&arch, "arch", "a", "", "architecture of app/images to pull")
	viper.BindPFlag("author", rootCmd.PersistentFlags().Lookup("author"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)

		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				// Config file not found; ignore error if desired
				fmt.Println("Not found")
			} else {
				// Config file was found but another error was produced
			}
		}
		// Config file found and successfully parsed
	} else {
		// TODO: look for config file at some system path, e.g. /usr/lib/composeapp/config.toml
	}
	if len(storeRoot) > 0 {
		config.StoreRoot = storeRoot
	} else {
		config.StoreRoot = viper.GetString("store.path")
		if len(config.StoreRoot) == 0 {
			homeDir, homeDirErr := os.UserHomeDir()
			if homeDirErr != nil {
				fmt.Printf("Failed to get a path to home directory," +
					" using the current directory as a store root directory\n")
				homeDir, homeDirErr = os.Getwd()
				DieNotNil(homeDirErr)
			}
			config.StoreRoot = path.Join(homeDir, ".composeapps/store")
		}
	}
	if len(composeRoot) > 0 {
		config.ComposeRoot = composeRoot
	} else {
		config.ComposeRoot = viper.GetString("compose.path")
		if len(config.ComposeRoot) == 0 {
			homeDir, homeDirErr := os.UserHomeDir()
			if homeDirErr != nil {
				fmt.Printf("Failed to get a path to home directory," +
					" using the current directory as a store root directory\n")
				homeDir, homeDirErr = os.Getwd()
				DieNotNil(homeDirErr)
			}
			config.ComposeRoot = path.Join(homeDir, ".composeapps/projects")
		}
	}
	cfg := dockercfg.LoadDefaultConfigFile(os.Stderr)
	if cfg == nil {
		os.Exit(1)
	}
	config.DockerCfg = cfg
	config.Platform = platforms.DefaultSpec()
	if len(arch) > 0 {
		config.Platform.Architecture = arch
	}
}
