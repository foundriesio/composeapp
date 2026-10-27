package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	BlobProvider interface {
		content.InfoProvider
		Type() BlobProviderType
		GetReadCloser(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error)
	}
	BlobProviderType string

	remoteBlobProvider struct {
		resolver remotes.Resolver
	}
	localBlobProvider struct {
		localFileProvider content.Store
	}
	storeBlobProvider struct {
		appStoreBlobRoot string
	}
	memoryBlobProvider struct {
		blobs map[digest.Digest][]byte
	}
	// storeFirstBlobProvider reads blobs from the store and the ones missing or
	// invalid there from the registry. It reports the remote type so a tree loaded
	// through it has the same nodes as one loaded from the registry. It serves only
	// the blobs under the store's blobs/sha256 directory: the manifest and bundle of
	// an app pulled by skopeo or aklite are kept under apps/ and are still read from
	// the registry.
	storeFirstBlobProvider struct {
		blobRoot string
		remote   BlobProvider
	}
	readCloserWrapper struct {
		reader io.Reader
		closer io.Closer
	}
)

const (
	BlobProviderTypeRemote BlobProviderType = "blob-provider:remote"
	BlobProviderTypeLocal  BlobProviderType = "blob-provider:local"
	BlobProviderTypeStore  BlobProviderType = "blob-provider:store"
	BlobProviderTypeMemory BlobProviderType = "blob-provider:memory"
)

func NewStoreBlobProvider(blobRoot string) BlobProvider {
	return &storeBlobProvider{
		appStoreBlobRoot: blobRoot,
	}
}

func NewLocalBlobProvider(fileProvider content.Store) BlobProvider {
	return &localBlobProvider{
		localFileProvider: fileProvider,
	}
}

type tokenProxyTripper struct {
	base http.RoundTripper
	host *url.URL
}

func (t tokenProxyTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBodyClosed := false
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				req.Body.Close()
			}
		}()
	}

	req2 := req.Clone(req.Context())
	req2.URL = t.host.JoinPath(req2.URL.Path)
	req2.Host = t.host.Host

	// req.Body is assumed to be closed by the base RoundTripper.
	reqBodyClosed = true
	return t.base.RoundTrip(req2)
}

func NewRemoteBlobProviderFromConfig(config *Config) BlobProvider {
	client := NewHttpClient(config.ConnectTimeout, config.ReadTimeout)
	var proxyConfig *ProxyConfig
	if config.Proxy != nil {
		// Get proxy configuration from config, if any.
		// If it is nil, no proxy will be used, and the Proxy() will print log messages accordingly.
		proxyConfig = config.Proxy()
	}
	if proxyConfig != nil {
		tpt := tokenProxyTripper{
			base: client.Transport,
			host: proxyConfig.ProxyURL,
		}
		if proxyConfig.ProxyCerts != nil {
			client.Transport.(*http.Transport).TLSClientConfig.RootCAs = proxyConfig.ProxyCerts
		}
		client.Transport = tpt
	}
	authorizer := NewRegistryAuthorizer(config.DockerCfg, client)
	resolver := NewResolver(authorizer, client)
	return newRemoteBlobProvider(resolver)
}

func newRemoteBlobProvider(resolver remotes.Resolver) BlobProvider {
	return &remoteBlobProvider{
		resolver: resolver,
	}
}

func NewStoreFirstBlobProviderFromConfig(config *Config) BlobProvider {
	return &storeFirstBlobProvider{
		blobRoot: GetBlobsRootFor(config.StoreRoot),
		remote:   NewRemoteBlobProviderFromConfig(config),
	}
}

func NewMemoryBlobProvider(blobs map[digest.Digest][]byte) BlobProvider {
	return &memoryBlobProvider{
		blobs: blobs,
	}
}

func (store *storeBlobProvider) Type() BlobProviderType {
	return BlobProviderTypeStore
}

func (store *storeBlobProvider) GetReadCloser(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	newOpts := opts
	p := GetSecureReadParams(opts...)
	if len(p.ExpectedDigest) == 0 {
		if len(p.Ref) > 0 {
			s, err := reference.Parse(p.Ref)
			if err != nil {
				return nil, err
			}
			p.ExpectedDigest = s.Digest()
			newOpts = append(newOpts, WithExpectedDigest(p.ExpectedDigest))
		} else {
			return nil, fmt.Errorf("missing parameters: either `SecureReadOpts.Ref` or `SecureReadOpts.ExpectedDigest` should be specified")
		}
	}
	f, err := os.Open(path.Join(store.appStoreBlobRoot, p.ExpectedDigest.Encoded()))
	if err != nil {
		return nil, err
	}
	if p.DisableSecureRead {
		// If secure read is disabled, we return the file object directly
		return f, nil
	} else {
		return NewSecureReadCloser(f, newOpts...)
	}
}

func (store *storeBlobProvider) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return content.Info{}, fmt.Errorf("not implemented")
}

func (l *localBlobProvider) Type() BlobProviderType {
	return BlobProviderTypeLocal
}

func (l *localBlobProvider) GetReadCloser(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	newOpts := opts
	p := GetSecureReadParams(opts...)
	if len(p.ExpectedDigest) == 0 {
		if len(p.Ref) > 0 {
			s, err := reference.Parse(p.Ref)
			if err != nil {
				return nil, err
			}
			p.ExpectedDigest = s.Digest()
			newOpts = append(newOpts, WithExpectedDigest(p.ExpectedDigest))
		} else {
			return nil, fmt.Errorf("missing parameters: either `SecureReadOpts.Ref` or `SecureReadOpts.ExpectedDigest` should be specified")
		}
	}
	ra, err := l.localFileProvider.ReaderAt(ctx, ocispec.Descriptor{Digest: p.ExpectedDigest})
	if err != nil {
		return nil, err
	}
	return NewSecureReadCloser(&readCloserWrapper{reader: content.NewReader(ra), closer: ra}, newOpts...)
}

func (l *localBlobProvider) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return l.localFileProvider.Info(ctx, dgst)
}

func (r *remoteBlobProvider) Type() BlobProviderType {
	return BlobProviderTypeRemote
}

func (r *remoteBlobProvider) GetReadCloser(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	p := GetSecureReadParams(opts...)
	if len(p.Ref) == 0 {
		return nil, fmt.Errorf("missing mandatory parameter `SecureReadParams.Ref`")
	}
	if p.DisableSecureRead {
		return r.getReadCloser(ctx, &p)
	} else {
		return r.getSecureReadCloser(ctx, &p, opts...)
	}
}

func (r *remoteBlobProvider) getSecureReadCloser(ctx context.Context, p *SecureReadParams, opts ...SecureReadOptions) (io.ReadCloser, error) {
	var desc ocispec.Descriptor
	var err error
	if len(p.ExpectedDigest) > 0 && p.ExpectedSize != 0 {
		desc.Digest = p.ExpectedDigest
		desc.Size = p.ExpectedSize
	} else {
		_, desc, err = r.resolver.Resolve(ctx, p.Ref)
		if err != nil {
			return nil, err
		}
	}
	f, err := r.resolver.Fetcher(ctx, p.Ref)
	if err != nil {
		return nil, err
	}
	sr, err := f.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	return NewSecureReadCloser(sr, append([]SecureReadOptions{WithExpectedDigest(desc.Digest), WithExpectedSize(desc.Size)}, opts...)...)
}

func (r *remoteBlobProvider) getReadCloser(ctx context.Context, p *SecureReadParams) (io.ReadCloser, error) {
	if p.Descriptor.Size == 0 || p.Descriptor.Digest == "" {
		return nil, fmt.Errorf("mandatory parameter `Descriptor` is not set or incomplete:" +
			" `Size` and `Digest` fields must be set")
	}
	f, err := r.resolver.Fetcher(ctx, p.Ref)
	if err != nil {
		return nil, err
	}
	return f.Fetch(ctx, p.Descriptor)
}

func (r *remoteBlobProvider) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return content.Info{}, fmt.Errorf("not implemented")
}

func (p *memoryBlobProvider) Type() BlobProviderType {
	return BlobProviderTypeMemory
}

func (p *memoryBlobProvider) GetReadCloser(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	params := GetSecureReadParams(opts...)
	if len(params.ExpectedDigest) == 0 {
		if len(params.Ref) > 0 {
			s, err := reference.Parse(params.Ref)
			if err != nil {
				return nil, err
			}
			params.ExpectedDigest = s.Digest()
		} else {
			return nil, fmt.Errorf("missing parameters: either `SecureReadOpts.Ref` or `SecureReadOpts.ExpectedDigest` should be specified")
		}
	}

	newOpts := opts
	newOpts = append(newOpts, WithExpectedDigest(params.ExpectedDigest))
	if b, ok := p.blobs[params.ExpectedDigest]; ok {
		return NewSecureReadCloser(io.NopCloser(bytes.NewReader(b)), newOpts...)
	} else {
		return nil, fmt.Errorf("blob %s not found", params.ExpectedDigest.String())
	}
}

func (p *memoryBlobProvider) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return content.Info{}, fmt.Errorf("not implemented")
}

func (p *storeFirstBlobProvider) Type() BlobProviderType {
	return BlobProviderTypeRemote
}

// GetReadCloser serves a stored blob only after reading it once to check its size
// and digest: a reader cannot switch to the registry once it has handed out bytes.
func (p *storeFirstBlobProvider) GetReadCloser(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	rc, storeErr := p.openVerified(ctx, opts...)
	if storeErr == nil {
		return rc, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	rc, err := p.remote.GetReadCloser(ctx, opts...)
	if err != nil {
		// The remote error stays the wrapped one so the callers classify it as before.
		return nil, fmt.Errorf("%w (store copy: %v)", err, storeErr)
	}
	return rc, nil
}

// openVerified opens the stored blob, checks its size and digest, and returns a
// verifying reader over the same file rewound to the start, so the blob served is
// the one checked even if its path is replaced in the meantime.
func (p *storeFirstBlobProvider) openVerified(ctx context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := GetSecureReadParams(opts...)
	d := params.ExpectedDigest
	if len(d) == 0 {
		if s, err := reference.Parse(params.Ref); err == nil {
			d = s.Digest()
		}
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	opts = append(opts[:len(opts):len(opts)], WithExpectedDigest(d))
	f, err := os.Open(path.Join(p.blobRoot, d.Encoded()))
	if err != nil {
		return nil, err
	}
	r, err := NewSecureReadCloser(io.NopCloser(f), opts...)
	if err == nil {
		err = discardAll(ctx, r)
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err == nil {
		r, err = NewSecureReadCloser(f, opts...)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func (p *storeFirstBlobProvider) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	return p.remote.Info(ctx, dgst)
}

// discardAll reads r to its end in 1 MiB steps and stops as soon as ctx is done,
// so a cancelled request does not hash a large blob to the end.
func discardAll(ctx context.Context, r io.Reader) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, r, 1<<20); err != nil {
			if err == io.EOF {
				return ctx.Err()
			}
			return err
		}
	}
}

func (w *readCloserWrapper) Read(p []byte) (n int, err error) {
	return w.reader.Read(p)
}
func (w *readCloserWrapper) Close() error {
	return w.closer.Close()
}
