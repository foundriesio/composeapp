package compose

import (
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"net"
	"net/http"
	"time"
)

func NewResolver(authorizer docker.Authorizer, connectTimeout time.Duration) remotes.Resolver {
	// Clone the default transport, so the default settings are preserved
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Override the DialContext with a custom TLS connection timeout
	transport.DialContext = (&net.Dialer{
		Timeout:   connectTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	// Set the response header timeout
	transport.ResponseHeaderTimeout = 30 * time.Second

	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(authorizer),
			docker.WithClient(&http.Client{Transport: transport})),
	})
}
