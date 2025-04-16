package compose

import (
	"github.com/docker/cli/cli/config/configfile"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"path"
	"time"
)

type (
	Config struct {
		StoreRoot      string
		ComposeRoot    string
		DockerCfg      *configfile.ConfigFile
		DockerHost     string
		Platform       specs.Platform
		ConnectTimeout time.Duration
		BlockSize      int64
		DBFilePath     string
		AppLoader      AppLoader
	}
)

func (c *Config) GetBlobsRoot() string {
	return GetBlobsRootFor(c.StoreRoot)
}

func (c *Config) NewRemoteBlobProvider() BlobProvider {
	authorizer := NewRegistryAuthorizer(c.DockerCfg, c.ConnectTimeout)
	resolver := NewResolver(authorizer, c.ConnectTimeout)
	return NewRemoteBlobProvider(resolver)
}

func GetBlobsRootFor(storeRoot string) string {
	return path.Join(storeRoot, "blobs", "sha256")
}
