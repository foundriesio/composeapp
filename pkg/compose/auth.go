package compose

import (
	"fmt"
	"net/http"

	"github.com/containerd/containerd/remotes/docker"
	"github.com/docker/cli/cli/config/configfile"
)

const (
	DefaultDockerRegistryHost = "registry-1.docker.io"
	DefaultDockerIndexHost    = "https://index.docker.io/v1/"
)

type (
	authCredsFunc func(string) (string, string, error)
)

func NewRegistryAuthorizer(cfg *configfile.ConfigFile, client *http.Client) docker.Authorizer {
	return docker.NewDockerAuthorizer(
		docker.WithAuthCreds(getAuthCreds(cfg)),
		docker.WithAuthClient(client),
	)
}

func getAuthCreds(cfg *configfile.ConfigFile) authCredsFunc {
	return func(host string) (string, string, error) {
		// containerd code translates "docker.io" into "registry-1.docker.io"
		if host == DefaultDockerRegistryHost {
			// but docker cli uses "https://index.docker.io/v1/" as the key to store auth config
			host = DefaultDockerIndexHost
		}
		creds, err := cfg.GetAllCredentials()
		if err != nil {
			return "", "", err
		}
		auth, ok := creds[host]
		if !ok {
			// no auth config found, return no error to try anonymous access
			return "", "", nil
		}
		if len(auth.Username) > 0 && len(auth.Password) > 0 {
			// basic auth
			return auth.Username, auth.Password, nil
		}
		if len(auth.IdentityToken) > 0 {
			// oauth auth
			return "", auth.IdentityToken, nil
		}
		return "", "", fmt.Errorf("neither user creds nor identity token is obtained for the given host: %s", host)
	}
}
