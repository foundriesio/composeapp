package compose

import (
	"crypto/x509"
	"net/url"
	"path/filepath"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	Config struct {
		StoreRoot           string
		ComposeRoot         string
		DockerCfg           *configfile.ConfigFile
		DockerHost          string
		Platform            specs.Platform
		ConnectTimeout      time.Duration
		ReadTimeout         time.Duration
		AppLoader           AppLoader
		AppStoreFactoryFunc func(c *Config) (AppStore, error)
		BlockSize           int64
		DBFilePath          string
		Proxy               ProxyProvider
	}
	ProxyConfig struct {
		ProxyURL   *url.URL
		ProxyCerts *x509.CertPool
	}
	ProxyProvider func() *ProxyConfig
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

func (c *Config) AppStoreFactory() (AppStore, error) {
	return c.AppStoreFactoryFunc(c)
}
