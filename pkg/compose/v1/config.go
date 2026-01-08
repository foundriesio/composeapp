package v1

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/platforms"
	dockercfg "github.com/docker/cli/cli/config"
	"github.com/foundriesio/composeapp/pkg/compose"
)

type (
	ConfigOpts struct {
		StoreRoot      string
		ComposeRoot    string
		ConnectTimeout time.Duration
		ReadTimeout    time.Duration
		SkopeoSupport  bool
		UpdateDBPath   string
		Proxy          compose.ProxyProvider
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

func WithProxy(proxy compose.ProxyProvider) ConfigOpt {
	return func(opts *ConfigOpts) {
		opts.Proxy = proxy
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

	// Set proxy provider specified via configuration options
	proxy := opts.Proxy
	// Override or set proxy provider if specified via environment variable
	if proxyFromEnv, err := getProxyProviderFromEnvIfSet(); err != nil {
		return nil, err
	} else if proxyFromEnv != nil {
		proxy = proxyFromEnv
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
		Proxy:      proxy,
	}, nil
}

func getProxyProviderFromEnvIfSet() (compose.ProxyProvider, error) {
	proxyEnv := os.Getenv("COMPOSE_APPS_PROXY")
	if len(proxyEnv) == 0 {
		return nil, nil
	}

	var proxyURL *url.URL
	var proxyCerts *x509.CertPool
	var err error

	if proxyURL, err = url.ParseRequestURI(proxyEnv); err != nil {
		return nil, fmt.Errorf("invalid COMPOSE_APPS_PROXY URL: %s: %w", proxyEnv, err)
	}
	if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" {
		return nil, fmt.Errorf("unsupported COMPOSE_APPS_PROXY URL scheme: %s", proxyEnv)
	}
	if proxyURL.Host == "" {
		return nil, fmt.Errorf("missing host in COMPOSE_APPS_PROXY URL: %s", proxyEnv)
	}
	proxyCa := os.Getenv("COMPOSE_APPS_PROXY_CA")
	if len(proxyCa) > 0 {
		proxyCerts = x509.NewCertPool()
		if b, err := os.ReadFile(proxyCa); err == nil {
			if ok := proxyCerts.AppendCertsFromPEM(b); !ok {
				return nil, fmt.Errorf("failed to parse COMPOSE_APPS_PROXY_CA: %s", proxyCa)
			}
		} else {
			return nil, fmt.Errorf("unable to read COMPOSE_APPS_PROXY_CA: %w", err)
		}
	}

	return func() *compose.ProxyConfig {
		return &compose.ProxyConfig{
			ProxyURL:   proxyURL,
			ProxyCerts: proxyCerts,
		}
	}, nil
}
