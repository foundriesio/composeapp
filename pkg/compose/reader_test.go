package compose

import (
	"context"
	"fmt"
	"github.com/opencontainers/go-digest"
	"testing"
)

func TestReaderWithReadLimit(t *testing.T) {
	blobData := []byte("some blob data")
	expectedDigest := digest.FromBytes(blobData)
	{
		// If a blob size is not known in advance just some upper limit for a given blob type is set
		b, err := ReadBlobWithReadLimit(context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: blobData}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()), 1024)
		if err != nil {
			t.Errorf("expected successful blob read, got %s", err.Error())
		}
		if string(b) != string(blobData) {
			t.Errorf("read blob doesn't match the expect one")
		}
	}
	{
		_, err := ReadBlobWithReadLimit(
			context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: []byte("some invalid blob data")}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()), 1024)
		if _, ok := err.(*ErrBlobDigestMismatch); !ok {
			t.Errorf("expected ErrBlobDigestMismatch error got: %s", err.Error())
		}
	}
	{
		_, err := ReadBlobWithReadLimit(context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: blobData}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()), int64(len(blobData)-1))
		if _, ok := err.(*ErrBlobSizeLimitExceed); !ok {
			t.Errorf("expected ErrBlobSizeLimitExceed error got: %s", err.Error())
		}
	}
}
func TestReaderWithTrustedSize(t *testing.T) {
	blobData := []byte("some blob data")
	expectedDigest := digest.FromBytes(blobData)
	{
		b, err := ReadBlob(context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: blobData}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()),
			expectedDigest, int64(len(blobData)))
		if err != nil {
			t.Errorf("expected successful blob read, got %s", err.Error())
		}
		if string(b) != string(blobData) {
			t.Errorf("read blob doesn't match the expect one")
		}
	}
	{
		_, err := ReadBlob(context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: []byte("some invalid blob data")}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()),
			expectedDigest, int64(len(blobData)))
		if _, ok := err.(*ErrBlobDigestMismatch); !ok {
			t.Errorf("expected ErrBlobDigestMismatch error got: %s", err.Error())
		}
	}
	{
		// Expected size is lower than actual blob size
		// The secure reader will read the expected amount of bytes and the actual blob size is higher,
		// hence we expect the digests mismatch.
		_, err := ReadBlob(context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: blobData}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()),
			expectedDigest, int64(len(blobData)-1))
		if _, ok := err.(*ErrBlobDigestMismatch); !ok {
			t.Errorf("expected ErrBlobDigestMismatch error got: %s", err.Error())
		}
	}
	{
		_, err := ReadBlob(context.Background(),
			NewMemoryBlobProvider(map[digest.Digest][]byte{expectedDigest: blobData}),
			fmt.Sprintf("host/factory/app@%s", expectedDigest.String()),
			expectedDigest, int64(len(blobData)+1))
		if _, ok := err.(*ErrBlobSizeMismatch); !ok {
			t.Errorf("expected ErrBlobSizeMismatch error got: %s", err.Error())
		}
	}
}
