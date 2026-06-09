package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// errInjected is the sentinel a fakeBlobProvider wraps when asked to fail a blob,
// so tests can assert error propagation with errors.Is.
var errInjected = errors.New("injected fetch failure")

// fakeReadSeekCloser serves blob bytes from memory and, on the first read, sleeps
// for `delay` to widen the window during which a fetch is "in flight" so the test
// can observe real overlap between concurrent workers.
type fakeReadSeekCloser struct {
	*bytes.Reader
	delay   time.Duration
	slept   bool
	onClose func()
}

func (f *fakeReadSeekCloser) Read(p []byte) (int, error) {
	if !f.slept {
		f.slept = true
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
	}
	return f.Reader.Read(p)
}

func (f *fakeReadSeekCloser) Close() error {
	if f.onClose != nil {
		f.onClose()
	}
	return nil
}

// fakeBlobProvider is an in-memory BlobProvider that tracks how many fetches are
// concurrently in flight and can inject a failure for a specific digest.
type fakeBlobProvider struct {
	blobs      map[digest.Digest][]byte
	failDigest digest.Digest
	readDelay  time.Duration

	mu        sync.Mutex
	active    int
	maxActive int
}

func (p *fakeBlobProvider) Type() BlobProviderType { return BlobProviderTypeMemory }

func (p *fakeBlobProvider) Info(_ context.Context, _ digest.Digest) (content.Info, error) {
	return content.Info{}, fmt.Errorf("not implemented")
}

func (p *fakeBlobProvider) GetReadCloser(_ context.Context, opts ...SecureReadOptions) (io.ReadCloser, error) {
	dgst := GetSecureReadParams(opts...).Descriptor.Digest
	if p.failDigest != "" && dgst == p.failDigest {
		return nil, fmt.Errorf("blob %s: %w", dgst, errInjected)
	}
	data, ok := p.blobs[dgst]
	if !ok {
		return nil, fmt.Errorf("blob %s not found", dgst)
	}
	p.enter()
	return &fakeReadSeekCloser{
		Reader:  bytes.NewReader(data),
		delay:   p.readDelay,
		onClose: p.leave,
	}, nil
}

func (p *fakeBlobProvider) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
}

func (p *fakeBlobProvider) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
}

func (p *fakeBlobProvider) maxConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

func newTestStore(t *testing.T) content.Store {
	t.Helper()
	ls, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create local store: %s", err)
	}
	return ls
}

// newTestBlob builds a fetchable blob descriptor whose digest matches its bytes so
// the local store's digest verification during CopyBlob succeeds.
func newTestBlob(data []byte) *BlobFetchProgress {
	dgst := digest.FromBytes(data)
	desc := &ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Digest:    dgst,
		Size:      int64(len(data)),
		URLs:      []string{"fake://" + dgst.String()},
	}
	return &BlobFetchProgress{
		BlobInfo: BlobInfo{
			Descriptor: desc,
			State:      BlobMissing,
			Type:       BlobTypeImageLayer,
		},
	}
}

func TestRunBlobFetchPoolRespectsWorkerLimit(t *testing.T) {
	const (
		workers  = 3
		numBlobs = 9
	)
	provider := &fakeBlobProvider{blobs: map[digest.Digest][]byte{}, readDelay: 100 * time.Millisecond}
	var blobs []*BlobFetchProgress
	for i := 0; i < numBlobs; i++ {
		data := []byte(fmt.Sprintf("limit-blob-%d-payload", i))
		provider.blobs[digest.FromBytes(data)] = data
		blobs = append(blobs, newTestBlob(data))
	}

	ls := newTestStore(t)
	if err := runBlobFetchPool(context.Background(), provider, ls, blobs, workers); err != nil {
		t.Fatalf("runBlobFetchPool returned an unexpected error: %s", err)
	}

	// The pool must never run more than `workers` fetches at once, yet with more
	// blobs than workers it must saturate the pool to exactly `workers`.
	if got := provider.maxConcurrency(); got != workers {
		t.Errorf("expected peak concurrency to equal the worker limit %d, got %d", workers, got)
	}

	for _, b := range blobs {
		if _, err := ls.Info(context.Background(), b.Descriptor.Digest); err != nil {
			t.Errorf("blob %s missing from store after fetch: %s", b.Descriptor.Digest, err)
		}
	}
}

func TestRunBlobFetchPoolFailsFast(t *testing.T) {
	provider := &fakeBlobProvider{blobs: map[digest.Digest][]byte{}}
	var blobs []*BlobFetchProgress
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("failfast-blob-%d", i))
		provider.blobs[digest.FromBytes(data)] = data
		blobs = append(blobs, newTestBlob(data))
	}
	// Inject a failure for one of the blobs.
	provider.failDigest = blobs[2].Descriptor.Digest

	ls := newTestStore(t)
	err := runBlobFetchPool(context.Background(), provider, ls, blobs, 2)
	if err == nil {
		t.Fatal("expected runBlobFetchPool to return an error, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("expected returned error to wrap the injected failure, got: %s", err)
	}
	// The failing blob must never be committed to the store.
	if _, infoErr := ls.Info(context.Background(), blobs[2].Descriptor.Digest); infoErr == nil {
		t.Errorf("failing blob %s should not be present in the store", blobs[2].Descriptor.Digest)
	}
}

func TestRunBlobFetchPoolFetchesAllBlobs(t *testing.T) {
	const workers = DefaultFetchWorkers
	provider := &fakeBlobProvider{blobs: map[digest.Digest][]byte{}}
	var blobs []*BlobFetchProgress
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("all-blob-%d-payload", i))
		provider.blobs[digest.FromBytes(data)] = data
		blobs = append(blobs, newTestBlob(data))
	}

	ls := newTestStore(t)
	if err := runBlobFetchPool(context.Background(), provider, ls, blobs, workers); err != nil {
		t.Fatalf("runBlobFetchPool returned an unexpected error: %s", err)
	}

	for _, b := range blobs {
		info, err := ls.Info(context.Background(), b.Descriptor.Digest)
		if err != nil {
			t.Errorf("blob %s missing from store: %s", b.Descriptor.Digest, err)
			continue
		}
		if info.Size != b.Descriptor.Size {
			t.Errorf("blob %s stored size %d, want %d", b.Descriptor.Digest, info.Size, b.Descriptor.Size)
		}
	}
}
