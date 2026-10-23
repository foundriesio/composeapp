package compose

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestStoreFirstBlobProvider(t *testing.T) {
	storeRoot := t.TempDir()
	blobs := map[digest.Digest][]byte{}
	newBlob := func(data string, stored []byte) digest.Digest {
		d := digest.FromString(data)
		blobs[d] = []byte(data)
		if stored != nil {
			if err := os.WriteFile(path.Join(storeRoot, d.Encoded()), stored, 0644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	// The remote serves every blob; the store holds a valid, a corrupt, a truncated
	// and no copy of them, so a blob read from the remote proves the fallback.
	storedOk := newBlob("stored ok", []byte("stored ok"))
	corrupt := newBlob("corrupt", []byte("CORRUPT"))
	truncated := newBlob("truncated", []byte("trunc"))
	missing := newBlob("missing", nil)
	p := &storeFirstBlobProvider{blobRoot: storeRoot, remote: NewMemoryBlobProvider(blobs)}
	if p.Type() != BlobProviderTypeRemote {
		t.Fatalf("expected type %s, got %s", BlobProviderTypeRemote, p.Type())
	}

	read := func(t *testing.T, opts ...SecureReadOptions) []byte {
		t.Helper()
		rc, err := p.GetReadCloser(context.Background(), opts...)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	for name, d := range map[string]digest.Digest{
		"valid in the store":     storedOk,
		"corrupt in the store":   corrupt,
		"truncated in the store": truncated,
		"missing in the store":   missing,
	} {
		t.Run(name, func(t *testing.T) {
			got := read(t, WithExpectedDigest(d), WithExpectedSize(int64(len(blobs[d]))))
			if !bytes.Equal(got, blobs[d]) {
				t.Fatalf("expected %q, got %q", blobs[d], got)
			}
		})
	}

	t.Run("served from the store", func(t *testing.T) {
		// Remove the remote copy so only the store can serve the blob.
		delete(blobs, storedOk)
		defer func() { blobs[storedOk] = []byte("stored ok") }()
		got := read(t, WithExpectedDigest(storedOk), WithExpectedSize(int64(len("stored ok"))))
		if string(got) != "stored ok" {
			t.Fatalf("expected %q, got %q", "stored ok", got)
		}
	})

	t.Run("digest taken from the ref", func(t *testing.T) {
		delete(blobs, storedOk)
		defer func() { blobs[storedOk] = []byte("stored ok") }()
		got := read(t, WithRef("registry:5000/factory/app@"+storedOk.String()), WithExpectedSize(int64(len("stored ok"))))
		if string(got) != "stored ok" {
			t.Fatalf("expected %q, got %q", "stored ok", got)
		}
	})

	t.Run("missing everywhere", func(t *testing.T) {
		if _, err := p.GetReadCloser(context.Background(), WithExpectedDigest(digest.FromString("absent")), WithExpectedSize(6)); err == nil {
			t.Fatal("expected an error for a blob missing in the store and the registry")
		}
	})
}
