package v1

import (
	"fmt"
	"github.com/containerd/containerd/platforms"
	dockercfg "github.com/docker/cli/cli/config"
	"github.com/foundriesio/composeapp/pkg/compose"
	"os"
	"path"
	"path/filepath"
	"time"
)

type (
	ConfigOpts struct {
		StoreRoot      string
		ComposeRoot    string
		ConnectTimeout time.Duration
		ReadTimeout    time.Duration
		SkopeoSupport  bool
		UpdateDBPath   string
	}
	ConfigOpt func(*ConfigOpts)
)

const (
	DefaultRootDir        = ".composeapps"
	DefaultStoreDir       = "store"
	DefaultComposeDir     = "projects"
	DefaultConnectTimeout = time.Duration(120) * time.Second
	DefaultReadTimeout    = time.Duration(900) * time.Second
	DefaultDBFileName     = "updates.db"
)

func WithUpdateDB(dbPath string) ConfigOpt {
	return func(opts *ConfigOpts) {
		opts.UpdateDBPath = dbPath
	}
}

func WithSkopeoSupport(skopeoSupport bool) ConfigOpt {
	return func(opts *ConfigOpts) {
		opts.SkopeoSupport = skopeoSupport
	}
}

func WithStoreRoot(storeRoot string) ConfigOpt {
	return func(opts *ConfigOpts) {
		opts.StoreRoot = storeRoot
	}
}

func WithComposeRoot(composeRoot string) ConfigOpt {
	return func(opts *ConfigOpts) {
		opts.ComposeRoot = composeRoot
	}
}

func WithConnectTimeout(timeout time.Duration) ConfigOpt {
	return func(opts *ConfigOpts) {
		opts.ConnectTimeout = timeout
	}
}

func NewDefaultConfig(options ...ConfigOpt) (*compose.Config, error) {
	opts := &ConfigOpts{
		ConnectTimeout: DefaultConnectTimeout,
		ReadTimeout:    DefaultReadTimeout,
	}
	for _, opt := range options {
		opt(opts)
	}

	var err error
	var homeDir string
	if len(opts.StoreRoot) == 0 || len(opts.ComposeRoot) == 0 {
		homeDir, err = os.UserHomeDir()
		if err != nil {
			// TODO: print log
			homeDir, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get the user's home and current working directory: %s",
					err.Error())
			}
		}
	}
	if len(opts.StoreRoot) == 0 {
		opts.StoreRoot = path.Join(homeDir, DefaultRootDir, DefaultStoreDir)
	}
	if len(opts.ComposeRoot) == 0 {
		opts.ComposeRoot = path.Join(homeDir, DefaultRootDir, DefaultComposeDir)
	}
	if len(opts.UpdateDBPath) == 0 {
		opts.UpdateDBPath = path.Join(opts.StoreRoot, DefaultDBFileName)
	}
	if _, err := os.Stat(opts.StoreRoot); os.IsNotExist(err) {
		if err := os.MkdirAll(opts.StoreRoot, 0755); err != nil {
			return nil, fmt.Errorf("failed to create app store root directory: %s", err.Error())
		}
	}
	if _, err := os.Stat(opts.ComposeRoot); os.IsNotExist(err) {
		if err := os.MkdirAll(opts.ComposeRoot, 0755); err != nil {
			return nil, fmt.Errorf("failed to create app compose root directory: %s", err.Error())
		}
	}

	// Get file system block size
	s, err := compose.GetFsStat(filepath.Dir(opts.StoreRoot))
	if err != nil {
		return nil, fmt.Errorf("failed to get file system stat; path: %s, err: %s", opts.StoreRoot, err.Error())
	}

	// Load docker config
	dockerCfg, err := dockercfg.Load("")
	if err != nil {
		return nil, fmt.Errorf("failed to load docker configuration: %s", err.Error())
	}

	platform := platforms.DefaultSpec()

	return &compose.Config{
		StoreRoot:      opts.StoreRoot,
		ComposeRoot:    opts.ComposeRoot,
		DockerCfg:      dockerCfg,
		Platform:       platform,
		ConnectTimeout: opts.ConnectTimeout,
		ReadTimeout:    opts.ReadTimeout,
		AppLoader:      NewAppLoader(),
		AppStoreFactoryFunc: func(c *compose.Config) (compose.AppStore, error) {
			return NewAppStore(c.StoreRoot, c.Platform, opts.SkopeoSupport)
		},
		BlockSize:  s.BlockSize,
		DBFilePath: opts.UpdateDBPath,
	}, nil
}
