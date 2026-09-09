//go:build publish

package internal

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/cli/cli/config"
	"github.com/docker/distribution"
	"github.com/docker/distribution/reference"
	distributionclient "github.com/docker/distribution/registry/client"
	"github.com/docker/distribution/registry/client/auth"
	"github.com/docker/distribution/registry/client/transport"
	registrytypes "github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/registry"
)

// AuthConfigResolver returns Auth Configuration for an index
type AuthConfigResolver func(ctx context.Context, index *registrytypes.IndexInfo) registrytypes.AuthConfig

type RegistryClient struct {
	authConfigResolver AuthConfigResolver
	insecureRegistry   bool
	userAgent          string
}

func ResolveAuthConfig(ctx context.Context, index *registrytypes.IndexInfo) registrytypes.AuthConfig {
	cfg := config.LoadDefaultConfigFile(os.Stderr)
	a, _ := cfg.GetAuthConfig(registry.GetAuthConfigKey(index))
	return registrytypes.AuthConfig(a)
}

func NewRegistryClient() RegistryClient {
	resolver := func(ctx context.Context, index *registrytypes.IndexInfo) registrytypes.AuthConfig {
		return ResolveAuthConfig(ctx, index)
	}

	return RegistryClient{
		authConfigResolver: resolver,
		insecureRegistry:   false,
		userAgent:          "Compose-Ref",
	}
}

func (c *RegistryClient) GetRepository(ctx context.Context, ref reference.Named) (distribution.Repository, error) {
	repoEndpoint, err := newDefaultRepositoryEndpoint(ref, c.insecureRegistry)
	if err != nil {
		return nil, err
	}

	return c.getRepositoryForReference(ctx, ref, repoEndpoint)
}

func (c *RegistryClient) getRepositoryForReference(ctx context.Context, ref reference.Named, repoEndpoint repositoryEndpoint) (distribution.Repository, error) {
	httpTransport, err := c.getHTTPTransportForRepoEndpoint(ctx, repoEndpoint)
	if err != nil {
		return nil, err
	}
	repoName, err := reference.WithName(repoEndpoint.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to parse repo name from %s: %w", ref, err)
	}
	return distributionclient.NewRepository(repoName, repoEndpoint.BaseURL(), httpTransport)
}

func (c *RegistryClient) getHTTPTransportForRepoEndpoint(ctx context.Context, repoEndpoint repositoryEndpoint) (http.RoundTripper, error) {
	httpTransport, err := getHTTPTransport(
		c.authConfigResolver(ctx, repoEndpoint.info.Index),
		repoEndpoint.endpoint,
		repoEndpoint.Name(),
		c.userAgent)
	if err != nil {
		return nil, fmt.Errorf("failed to configure transport: %w", err)
	}
	return httpTransport, nil
}

// getHTTPTransport builds a transport for use in communicating with a registry
func getHTTPTransport(authConfig registrytypes.AuthConfig, endpoint registry.APIEndpoint, repoName string, userAgent string) (http.RoundTripper, error) {
	// get the http transport, this will be used in a client to upload manifest
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     endpoint.TLSConfig,
		DisableKeepAlives:   true,
	}

	modifiers := registry.Headers(userAgent, http.Header{})
	authTransport := transport.NewTransport(base, modifiers...)
	challengeManager, err := registry.PingV2Registry(endpoint.URL, authTransport)
	if err != nil {
		return nil, fmt.Errorf("error pinging v2 registry: %w", err)
	}
	if authConfig.RegistryToken != "" {
		passThruTokenHandler := &existingTokenHandler{token: authConfig.RegistryToken}
		modifiers = append(modifiers, auth.NewAuthorizer(challengeManager, passThruTokenHandler))
	} else {
		creds := registry.NewStaticCredentialStore(&authConfig)
		tokenHandler := auth.NewTokenHandler(authTransport, creds, repoName, "push", "pull")
		basicHandler := auth.NewBasicHandler(creds)
		modifiers = append(modifiers, auth.NewAuthorizer(challengeManager, tokenHandler, basicHandler))
	}
	return transport.NewTransport(base, modifiers...), nil
}

type existingTokenHandler struct {
	token string
}

func (th *existingTokenHandler) AuthorizeRequest(req *http.Request, params map[string]string) error {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", th.token))
	return nil
}

func (th *existingTokenHandler) Scheme() string {
	return "bearer"
}

type repositoryEndpoint struct {
	info     *registry.RepositoryInfo
	endpoint registry.APIEndpoint
}

// Name returns the repository name
func (r repositoryEndpoint) Name() string {
	repoName := r.info.Name.Name()
	// If endpoint does not support CanonicalName, use the RemoteName instead
	if r.endpoint.TrimHostname {
		repoName = reference.Path(r.info.Name)
	}
	return repoName
}

// BaseURL returns the endpoint url
func (r repositoryEndpoint) BaseURL() string {
	return r.endpoint.URL.String()
}

// insecureRegistriesEnvVar mirrors pkg/compose.InsecureRegistriesEnvVar (this package can't
// import pkg/compose -- pkg/compose already imports internal, so that would cycle).
const insecureRegistriesEnvVar = "COMPOSECTL_INSECURE_REGISTRIES"

func newRegistryService() (*registry.Service, error) {
	var insecureRegistries []string
	for _, h := range strings.Split(os.Getenv(insecureRegistriesEnvVar), ",") {
		if h != "" {
			insecureRegistries = append(insecureRegistries, h)
		}
	}
	return registry.NewService(registry.ServiceOptions{InsecureRegistries: insecureRegistries})
}

func newDefaultRepositoryEndpoint(ref reference.Named, insecure bool) (repositoryEndpoint, error) {
	registryService, err := newRegistryService()
	if err != nil {
		return repositoryEndpoint{}, err
	}
	// Use this same, InsecureRegistries-aware service instance for both calls below:
	// registry.ParseRepositoryInfo(ref) (a package-level convenience function) always resolves
	// repoInfo.Index.Secure against a separate, empty-options service built once at package
	// init, so it would never see our InsecureRegistries list and always report the host as
	// secure/TLS-required. ResolveRepository (the instance method) resolves it against this
	// service's own config instead, matching the endpoint scheme LookupPushEndpoints then picks
	// below.
	repoInfo, err := registryService.ResolveRepository(ref)
	if err != nil {
		return repositoryEndpoint{}, err
	}
	endpoint, err := getDefaultEndpointFromRepoInfo(registryService, repoInfo)
	if err != nil {
		return repositoryEndpoint{}, err
	}
	if insecure {
		endpoint.TLSConfig.InsecureSkipVerify = true
	}
	return repositoryEndpoint{info: repoInfo, endpoint: endpoint}, nil
}

func getDefaultEndpointFromRepoInfo(registryService *registry.Service, repoInfo *registry.RepositoryInfo) (registry.APIEndpoint, error) {
	endpoints, err := registryService.LookupPushEndpoints(reference.Domain(repoInfo.Name))
	if err != nil {
		return registry.APIEndpoint{}, err
	}
	// Default to the highest priority endpoint to return
	endpoint := endpoints[0]
	if !repoInfo.Index.Secure {
		for _, ep := range endpoints {
			if ep.URL.Scheme == "http" {
				endpoint = ep
			}
		}
	}
	return endpoint, nil
}
