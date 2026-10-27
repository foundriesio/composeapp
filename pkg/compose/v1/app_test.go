package v1

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/opencontainers/go-digest"
)

func TestWalkVerifiedTarChecksWholeBlob(t *testing.T) {
	blob := tarGz(t, "services: {}\n", 4096)
	blobDigest := digest.FromBytes(blob)

	walk := func(b []byte) (string, error) {
		src, err := compose.NewSecureReadCloser(io.NopCloser(bytes.NewReader(b)),
			compose.WithExpectedDigest(blobDigest), compose.WithExpectedSize(int64(len(b))))
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var content []byte
		err = walkVerifiedTar(src, func(hdr *tar.Header, tr io.Reader) error {
			content, err = io.ReadAll(tr)
			return err
		})
		return string(content), err
	}

	if _, err := walk(blob); err != nil {
		t.Fatalf("expected the original blob to pass, got: %s", err)
	}

	// The registry serves another compose file, padded to the size of the original. The tar walk
	// ends before the padding and the gzip trailer, so it alone would accept the substitute.
	forged := tarGz(t, "services: {evil: {image: evil}}\n", 0)
	if len(forged) > len(blob) {
		t.Fatalf("forged bundle is larger than the original: %d > %d", len(forged), len(blob))
	}
	forged = append(forged, make([]byte, len(blob)-len(forged))...)
	var mismatch *compose.ErrBlobDigestMismatch
	if _, err := walk(forged); !errors.As(err, &mismatch) {
		t.Fatalf("expected ErrBlobDigestMismatch for a same-size forged blob, got: %v", err)
	}
}

func tarGz(t *testing.T, compose string, padding int64) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "docker-compose.yml", Mode: 0644, Size: int64(len(compose))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(compose)); err != nil {
		t.Fatal(err)
	}
	// Random padding makes the archive larger than a forged one with a longer compose file.
	if err := tw.WriteHeader(&tar.Header{Name: "padding", Mode: 0644, Size: padding}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(tw, rand.Reader, padding); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
