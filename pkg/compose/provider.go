package compose

import (
	"bytes"
	"context"
	"fmt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"os"
	"path"
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

func NewRemoteBlobProviderFromConfig(config *Config) BlobProvider {
	client := NewHttpClient(config.ConnectTimeout, config.ReadTimeout)
	authorizer := NewRegistryAuthorizer(config.DockerCfg, client)
	resolver := NewResolver(authorizer, client)
	return newRemoteBlobProvider(resolver)
}

func newRemoteBlobProvider(resolver remotes.Resolver) BlobProvider {
	return &remoteBlobProvider{
		resolver: resolver,
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

func (w *readCloserWrapper) Read(p []byte) (n int, err error) {
	return w.reader.Read(p)
}
func (w *readCloserWrapper) Close() error {
	return w.closer.Close()
}
