package compose

import (
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"net"
	"net/http"
	"time"
)

func NewResolver(authorizer docker.Authorizer, connectTimeout time.Duration) remotes.Resolver {
	ropts := []docker.RegistryOpt{
		docker.WithAuthorizer(authorizer),
		docker.WithClient(&http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout: connectTimeout,
				}).DialContext,
			},
		}),
	}
	//TODO: consider using options.Hosts = config.ConfigureHosts(ctx, hostOptions)
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(ropts...),
	},
	)
}
