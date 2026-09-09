package compose

import (
	"net/http"
	"os"
	"strings"

	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
)

// InsecureRegistriesEnvVar lets a deployment opt specific registry hosts into plain HTTP for
// pull/inspect, beyond the localhost default below -- e.g. a self-hosted test registry reached
// by a non-loopback hostname (a container's own DNS name, not literally "localhost"), where a
// real TLS cert isn't practical to set up. Comma-separated host[:port] list, matching each
// resolved host exactly (mirrors dockerd's own --insecure-registry convention, just scoped to
// exact hosts rather than CIDRs since that's all pull/publish need here).
const InsecureRegistriesEnvVar = "COMPOSECTL_INSECURE_REGISTRIES"

func isPlainHTTPHost(host string) (bool, error) {
	if match, err := docker.MatchLocalhost(host); match || err != nil {
		return match, err
	}
	for _, h := range strings.Split(os.Getenv(InsecureRegistriesEnvVar), ",") {
		if h != "" && h == host {
			return true, nil
		}
	}
	return false, nil
}

func NewResolver(authorizer docker.Authorizer, client *http.Client) remotes.Resolver {
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(authorizer),
			docker.WithClient(client),
			// Match Docker's own long-standing "localhost is insecure" default (moby's
			// registry/config.go: "Localhost is by default considered as an insecure
			// registry"), which `composectl publish` already gets for free via
			// docker/docker/registry (internal/reg_client.go's
			// getDefaultEndpointFromRepoInfo -> registry.NewService ->
			// LookupPushEndpoints). Without this, `pull`/`inspect` require real HTTPS
			// even for localhost, with no override, so they disagree with `publish` on
			// a bare local registry. COMPOSECTL_INSECURE_REGISTRIES extends the same
			// treatment to specific non-localhost hosts when needed.
			docker.WithPlainHTTP(isPlainHTTPHost),
		),
	})
}
