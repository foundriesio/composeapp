package compose

import (
	"context"
	"fmt"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"io"
)

type (
	SecureReadOptions func(opts *SecureReadParams)

	SecureReadParams struct {
		Ref            string
		ExpectedDigest digest.Digest
		ExpectedSize   int64
		ReadLimit      int64
		Descriptor     ocispec.Descriptor
	}

	ErrBlobDigestMismatch struct {
		Expected   digest.Digest
		Calculated digest.Digest
	}

	ErrBlobSizeMismatch struct {
		Expected int64
		Read     int64
	}

	ErrBlobSizeLimitExceed struct {
		Limit int64
	}

	secureReader struct {
		SecureReadParams
		closer    io.Closer
		reader    *io.LimitedReader
		digester  digest.Digester
		readBytes int64
	}
)

func GetSecureReadParams(opts ...SecureReadOptions) (p SecureReadParams) {
	for _, o := range opts {
		o(&p)
	}
	return
}

func (e *ErrBlobDigestMismatch) Error() string {
	return fmt.Sprintf("blob digest mismatch; expected: %s, got: %s", e.Expected, e.Calculated)
}
func (e *ErrBlobSizeMismatch) Error() string {
	return fmt.Sprintf("blob size mismatch; expected: %d, got: %d", e.Expected, e.Read)
}
func (e *ErrBlobSizeLimitExceed) Error() string {
	return fmt.Sprintf("blob size limit reached; limit: %d", e.Limit)
}

func NewSecureReadCloser(srcReadCloser io.ReadCloser, opts ...SecureReadOptions) (io.ReadCloser, error) {
	p := GetSecureReadParams(opts...)
	if p.ExpectedSize > 0 {
		if p.ReadLimit > 0 {
			if p.ReadLimit < p.ExpectedSize {
				return nil, fmt.Errorf("read limit must be higher than or equal to expected size; limit: %d, expected size: %d", p.ReadLimit, p.ExpectedSize)
			}
		} else {
			p.ReadLimit = p.ExpectedSize
		}
	}
	if p.ReadLimit == 0 {
		return nil, fmt.Errorf("neither `SecureReadParams.ReadLimit` not `SecureReadParams.ExpectedSize` is set")
	}
	return &secureReader{
		SecureReadParams: p,
		closer:           srcReadCloser,
		reader:           &io.LimitedReader{R: srcReadCloser, N: p.ReadLimit},
		digester:         digest.Canonical.Digester(),
		readBytes:        0,
	}, nil
}

func (r *secureReader) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	if n > 0 {
		r.readBytes += int64(n)
		if _, err := r.digester.Hash().Write(p[:n]); err != nil {
			return n, fmt.Errorf("failed to write data to hasher: %s", err)
		}
	}
	if err == io.EOF {
		if r.isExpectedSize() {
			// Limited reader makes sure that up to the expected size bytes are read, hence the reader
			// can read <= r.opts.ExpectedSize.
			// The value
			if r.readBytes < r.ExpectedSize {
				err = &ErrBlobSizeMismatch{Expected: r.ExpectedSize, Read: r.readBytes}
			} else if r.readBytes > r.ExpectedSize {
				//panic, this should NOT happen
				panic(fmt.Errorf("read more than expected bytes; expected: %d, read: %d", r.ExpectedSize, r.readBytes))
			}
		} else if r.reader.N == 0 {
			// if the expected size is not known and not set then the reader reads up to the read limit data.
			// If read bytes == the set read limit then we consider as a specific error and don't check digest
			// We need to determine/distinguish this error type to let a user know that a target blob size is
			// higher than the set read limit.
			err = &ErrBlobSizeLimitExceed{r.ReadLimit}
		}
		if err == io.EOF {
			// If it is still EOF and no blob size or limit reaching issue then check the read data hash/digest
			calculatedDigest := r.digester.Digest()
			if calculatedDigest != r.ExpectedDigest {
				err = &ErrBlobDigestMismatch{Expected: r.ExpectedDigest, Calculated: calculatedDigest}
			}
		}
	}
	return
}

func (r *secureReader) Close() error {
	return r.closer.Close()
}

func WithRef(ref string) func(opts *SecureReadParams) {
	return func(opts *SecureReadParams) {
		opts.Ref = ref
	}
}

func WithExpectedSize(size int64) func(opts *SecureReadParams) {
	return func(opts *SecureReadParams) {
		opts.ExpectedSize = size
	}
}
func WithExpectedDigest(digest digest.Digest) func(opts *SecureReadParams) {
	return func(opts *SecureReadParams) {
		opts.ExpectedDigest = digest
	}
}
func WithReadLimit(limit int64) func(opts *SecureReadParams) {
	return func(opts *SecureReadParams) {
		opts.ReadLimit = limit
	}
}

func WithDescriptor(desc ocispec.Descriptor) func(opts *SecureReadParams) {
	return func(opts *SecureReadParams) {
		opts.Descriptor = desc
	}
}

// TODO: the following three functions can be unified

func ReadBlob(ctx context.Context, provider BlobProvider, ref string, digest digest.Digest, size int64) ([]byte, error) {
	// This is intended only for secure reading of small blobs since the way it reads and copies data is not optimal
	// as well as keeping gigabytes of data in memory is suboptimal
	if size <= 0 {
		return nil, fmt.Errorf("invalid blob size specified: %d", size)
	}
	r, err := provider.GetReadCloser(ctx, WithRef(ref), WithExpectedDigest(digest), WithExpectedSize(size))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// TODO: io.ReadAll reads by 512 chunks, it might be not quite effective, better to increase buffer size
	return io.ReadAll(r)
}

func ReadBlobWithResolving(ctx context.Context, provider BlobProvider, ref string, size int64) ([]byte, error) {
	// This is intended only for secure reading of small blobs since the way it reads and copies data is not optimal
	// as well as keeping gigabytes of data in memory is suboptimal
	if size <= 0 {
		return nil, fmt.Errorf("invalid blob size specified: %d", size)
	}
	r, err := provider.GetReadCloser(ctx, WithRef(ref), WithExpectedSize(size))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// TODO: io.ReadAll reads by 512 chunks, it might be not quite effective, better to increase buffer size
	return io.ReadAll(r)
}

func ReadBlobWithReadLimit(ctx context.Context, provider BlobProvider, ref string, limit int64) ([]byte, error) {
	// This is intended only for secure reading of small blobs since the way it reads and copies data is not optimal
	// as well as keeping gigabytes of data in memory is suboptimal
	if limit <= 0 {
		return nil, fmt.Errorf("invalid read limit is specified: %d", limit)
	}

	r, err := provider.GetReadCloser(ctx, WithRef(ref), WithReadLimit(limit))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// TODO: io.ReadAll reads by 512 chunks, it might be not quite effective, better to increase buffer size
	return io.ReadAll(r)
}

func (r *secureReader) isExpectedSize() bool {
	return r.ExpectedSize > 0
}
