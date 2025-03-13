package compose

import (
	"github.com/docker/cli/cli/config/configfile"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	Config struct {
		StoreRoot   string
		ComposeRoot string
		DockerCfg   *configfile.ConfigFile
		DockerHost  string
		Platform    specs.Platform
		ConnectTime int
		BlockSize   int64
		DBFilePath  string
		AppLoader   AppLoader
	}
)
