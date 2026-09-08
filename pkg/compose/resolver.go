package compose

import (
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"net/http"
)

func NewResolver(authorizer docker.Authorizer, client *http.Client) remotes.Resolver {
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(authorizer),
			docker.WithClient(client),
			// Match Docker's own long-standing "localhost is insecure" default
			// (moby's registry/config.go: "Localhost is by default considered as an
			// insecure registry"), which `composectl publish` already gets for free
			// via docker/docker/registry (internal/reg_client.go's
			// getDefaultEndpointFromRepoInfo -> registry.NewService ->
			// LookupPushEndpoints). Without this, `pull` requires real HTTPS even
			// for localhost, with no override, so the two commands disagree on a
			// bare local registry.
			docker.WithPlainHTTP(docker.MatchLocalhost),
		),
	})
}
