package compose

import (
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
)

func NewResolver(authorizer docker.Authorizer) remotes.Resolver {
	ropts := []docker.RegistryOpt{
		docker.WithAuthorizer(authorizer),
	}
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(ropts...),
	},
	)
}
