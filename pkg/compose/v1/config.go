package v1

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	dockercfg "github.com/docker/cli/cli/config"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/sirupsen/logrus"
	"os"
	"path"
	"path/filepath"
)

const (
	AppWorkDir    = ".composeapps"
	AppStoreDir   = "store"
	AppProjectDir = "projects"

	DefaultFSBlockSize = 4096
)

func NewDefaultConfig() (*compose.Config, error) {
	// Determine the app store and compose/project directories
	rootDir, err := os.UserHomeDir()
	if err != nil {
		logrus.Errorf("failed to get a path to home directory: %s", err.Error())
		logrus.Info("setting the current directory as a compose app root directory...")
		rootDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to determine a compose app root directory: %s", err.Error())
		}
	}

	appWorkDir := path.Join(rootDir, AppWorkDir)
	_, err = os.Stat(appWorkDir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(appWorkDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create app store root directory: %s", err.Error())
		}
	}

	storeRoot := path.Join(appWorkDir, AppStoreDir)
	composeRoot := path.Join(appWorkDir, AppProjectDir)
	dbFilePath := path.Join(appWorkDir, "updates.db")

	// Determine the docker host

	// Load docker config
	dockerCfg, err := dockercfg.Load("")
	if err != nil {
		return nil, fmt.Errorf("failed to load docker configuration: %s", err.Error())
	}

	// Get file system block size
	var blockSize int64 = DefaultFSBlockSize
	s, err := compose.GetFsStat(filepath.Dir(storeRoot))
	if err != nil {
		logrus.Errorf("failed to obtain the file system block size: %s\n", err.Error())
		logrus.Info("assuming the file system block size is 4096 bytes ")
	} else {
		blockSize = s.BlockSize
	}

	// Obtain platform info
	platform := platforms.DefaultSpec()

	return &compose.Config{
		StoreRoot:   storeRoot,
		ComposeRoot: composeRoot,
		DockerCfg:   dockerCfg,
		Platform:    platform,
		BlockSize:   blockSize,
		DBFilePath:  dbFilePath,
		AppLoader:   NewAppLoader(),
	}, nil
}
