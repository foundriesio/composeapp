package compose

import (
	"github.com/docker/cli/cli/config/configfile"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"path/filepath"
	"time"
)

type (
	Config struct {
		StoreRoot       string
		ComposeRoot     string
		DockerCfg       *configfile.ConfigFile
		DockerHost      string
		Platform        specs.Platform
		ConnectTimeout  time.Duration
		AppLoader       AppLoader
		AppStoreFactory func() (AppStore, error)
		BlockSize       int64
		DBFilePath      string
	}
)

func (c *Config) GetAppComposeDir(appName string) string {
	return filepath.Join(c.ComposeRoot, appName)
}

func (c *Config) GetBlobsRoot() string {
	return GetBlobsRootFor(c.StoreRoot)
}

func GetBlobsRootFor(storeRoot string) string {
	return filepath.Join(storeRoot, "blobs", "sha256")
}
