package compose

import (
	"context"
	"fmt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/remotes"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"strings"
)

type (
	BlobState int
	BlobType  string
	BlobInfo  struct {
		Descriptor  *ocispec.Descriptor
		State       BlobState
		Type        BlobType
		StoreSize   int64
		RuntimeSize int64
	}
	BlobsStatus map[digest.Digest]BlobInfo
)

const (
	BlobStateUndefined BlobState = iota
	BlobOk
	BlobMissing
	BlobSizeInvalid
	BlobDigestInvalid

	BlobTypeUnknown       BlobType = "unknown blob type"
	BlobTypeAppManifest   BlobType = "app manifest"
	BlobTypeAppBundle              = "app bundle"
	BlobTypeAppLayersMeta          = "app meta"
	BlobTypeImageIndex             = "index"
	BlobTypeImageManifest          = "manifest"
	BlobTypeImageConfig            = "config"
	BlobTypeImageLayer             = "layer"
)

func (s BlobState) String() string {
	var ret string
	switch s {
	case BlobOk:
		ret = "OK"
	case BlobMissing:
		ret = "missing"
	case BlobSizeInvalid:
		ret = "invalid size"
	case BlobDigestInvalid:
		ret = "invalid hash"
	default:
		ret = "undefined"
	}
	return ret
}

func ErrToBlobState(err error) BlobState {
	state := BlobStateUndefined
	if err != nil && strings.Contains(err.Error(), "blob not found") {
		return BlobMissing
	}
	switch err.(type) {
	case nil:
		{
			state = BlobOk
		}
	case *ErrBlobSizeMismatch:
		{
			state = BlobSizeInvalid
		}
	case *ErrBlobDigestMismatch:
		{
			state = BlobDigestInvalid
		}
	}
	return state
}

func CheckBlob(ctx context.Context, provider BlobProvider, opts ...SecureReadOptions) (BlobState, error) {
	r, err := provider.GetReadCloser(ctx, opts...)
	if err == nil {
		defer r.Close()
		_, err = io.Copy(io.Discard, r)
	}
	state := ErrToBlobState(err)
	if state != BlobStateUndefined {
		err = nil
	}
	return state, err
}

func FetchBlob(ctx context.Context, resolver remotes.Resolver, ref string, desc ocispec.Descriptor, store content.Store, force bool) error {
	var err error
	var w content.Writer
	for {
		w, err = content.OpenWriter(ctx, store, content.WithRef(ref), content.WithDescriptor(desc))
		if err != nil {
			if force && errdefs.IsAlreadyExists(err) {
				if err := store.Delete(ctx, desc.Digest); err == nil {
					continue
				}
			} else {
				return fmt.Errorf("failed to open writer: %w", err)
			}
		}
		break
	}
	if err != nil {
		return err
	}
	defer w.Close()

	f, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		return err
	}
	r, err := f.Fetch(ctx, desc)
	if err != nil {
		return err
	}
	defer r.Close()

	return content.Copy(ctx, w, r, desc.Size, desc.Digest)
}
