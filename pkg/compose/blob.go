package compose

import (
	"context"
	"errors"
	"fmt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
	"os"
	"strings"
)

type (
	BlobState int
	BlobType  string
	BlobInfo  struct {
		Descriptor   *ocispec.Descriptor `json:"descriptor"`
		State        BlobState           `json:"state"`
		Type         BlobType            `json:"type"`
		StoreSize    int64               `json:"store_size"`
		RuntimeSize  int64               `json:"runtime_size"`
		BytesFetched int64               `json:"bytes_fetched"`
	}

	ctxKeyType string
)

const (
	BlobStateUndefined BlobState = iota
	BlobOk
	BlobFetching
	BlobMissing
	BlobSizeInvalid
	BlobDigestInvalid

	BlobTypeUnknown          BlobType = "unknown blob type"
	BlobTypeAppManifest      BlobType = "app manifest"
	BlobTypeAppBundle        BlobType = "app bundle"
	BlobTypeAppIndex         BlobType = "app index"
	BlobTypeAppLayersMeta    BlobType = "app meta"
	BlobTypeImageIndex       BlobType = "index"
	BlobTypeSkopeoImageIndex BlobType = "skopeo index"
	BlobTypeImageManifest    BlobType = "manifest"
	BlobTypeImageConfig      BlobType = "config"
	BlobTypeImageLayer       BlobType = "layer"

	ctxKeyBlobType ctxKeyType = "blob:type"
)

func (bi *BlobInfo) HasRef() bool {
	return len(bi.Descriptor.URLs) > 0
}

func (bi *BlobInfo) Ref() string {
	if bi.HasRef() {
		return bi.Descriptor.URLs[0]
	}
	return ""
}

func WithBlobType(ctx context.Context, blobType BlobType) context.Context {
	return context.WithValue(ctx, ctxKeyBlobType, blobType)
}

func GetBlobType(ctx context.Context) BlobType {
	if blobType, ok := ctx.Value(ctxKeyBlobType).(BlobType); ok {
		return blobType
	}
	return ""
}

func (s BlobState) String() string {
	var ret string
	switch s {
	case BlobOk:
		ret = "OK"
	case BlobFetching:
		ret = "fetching"
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
	if err != nil && (strings.Contains(err.Error(), "not found") || errors.Is(err, os.ErrNotExist)) {
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

func CheckBlob(ctx context.Context, provider BlobProvider, dgst digest.Digest, opts ...SecureReadOptions) (BlobState, error) {
	var checkErr error
	params := GetSecureReadParams(opts...)
	if len(params.ExpectedDigest) == 0 {
		info, err := provider.Info(ctx, dgst)
		if err == nil && info.Size != params.ExpectedSize {
			err = &ErrBlobSizeMismatch{
				Expected: params.ExpectedSize,
				Read:     info.Size,
			}
		}
		checkErr = err
	} else {
		r, err := provider.GetReadCloser(ctx, opts...)
		if err == nil {
			defer r.Close()
			_, err = io.Copy(io.Discard, r)
		}
		checkErr = err
	}
	state := ErrToBlobState(checkErr)
	if state != BlobStateUndefined {
		checkErr = nil
	}
	return state, checkErr
}

func CopyBlob(ctx context.Context, r io.ReadCloser, ref string, desc ocispec.Descriptor, store content.Store, force bool) error {
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
	return content.Copy(ctx, w, r, desc.Size, desc.Digest)
}
