package compose

import (
	"github.com/containerd/containerd/remotes/docker"
	"github.com/docker/cli/cli/config/configfile"
)

func NewRegistryAuthorizer(cfg *configfile.ConfigFile) docker.Authorizer {
	return docker.NewDockerAuthorizer(docker.WithAuthCreds(getAuthCreds(cfg)))
}

type (
	authCredsFunc func(string) (string, string, error)
)

func getAuthCreds(cfg *configfile.ConfigFile) authCredsFunc {
	return func(host string) (string, string, error) {
		auth, err := cfg.GetAuthConfig(host)
		if err != nil {
			return "", "", err
		}
		return auth.Username, auth.Password, nil
	}
}
