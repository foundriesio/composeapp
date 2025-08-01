package compose

import (
	"context"
	"errors"
	"fmt"
	composetypes "github.com/compose-spec/compose-go/types"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"strings"
)

type (
	AppRef struct {
		Spec   reference.Spec
		Repo   string
		Name   string
		Tag    string
		Digest digest.Digest
	}

	AppBundleErrs map[string]string
	AppTree       TreeNode
	App           interface {
		Name() string
		Tree() *AppTree
		NodeCount() int
		Ref() *AppRef
		HasLayersMeta(arch string) bool
		GetBlobRuntimeSize(desc *ocispec.Descriptor, arch string, blockSize int64) int64
		GetComposeRoot() *TreeNode
		GetCompose(ctx context.Context, provider BlobProvider) (*composetypes.Project, error)
		CheckComposeInstallation(ctx context.Context, provider BlobProvider, installationRootDir string) (AppBundleErrs, error)
	}
	AppLoader interface {
		LoadAppTree(context.Context, BlobProvider, platforms.MatchComparer, string) (App, error)
	}
)

const (
	ctxKeyAppRef ctxKeyType = "app:ref"
)

var (
	ErrAppNotFound      = errors.New("app not found")
	ErrAppHasNoIndex    = errors.New("app has no bundle index")
	ErrAppIndexNotFound = errors.New("app blob index is not found")
)

func WithAppRef(ctx context.Context, ref *AppRef) context.Context {
	return context.WithValue(ctx, ctxKeyAppRef, ref)
}

func GetAppRef(ctx context.Context) *AppRef {
	if appRef, ok := ctx.Value(ctxKeyAppRef).(*AppRef); ok {
		return appRef
	}
	return nil
}

func ParseAppRef(ref string) (*AppRef, error) {
	s, err := reference.Parse(ref)
	if err != nil {
		return nil, err
	}
	hostNameLen := len(s.Hostname())
	if hostNameLen == len(ref) {
		return nil, fmt.Errorf("invalid app reference: digest must be specified (host/repo/name@sha256:<hash>)")
	}
	appName := s.Locator[hostNameLen+1:]
	i := strings.Index(appName, "/")
	repo := ""
	if i > 0 {
		repo = appName[:i]
		appName = appName[i+1:]
	}
	t, d := reference.SplitObject(s.Object)
	return &AppRef{
		Spec:   s,
		Repo:   repo,
		Name:   appName,
		Tag:    t,
		Digest: d,
	}, nil
}

func (r *AppRef) String() string {
	return r.Spec.String()
}

func (r *AppRef) GetBlobRef(digest digest.Digest) string {
	return r.Spec.Locator + "@" + digest.String()
}

func (t *AppTree) Walk(fn NodeProcessor) error {
	return (*TreeNode)(t).Walk(fn)
}

func (t *AppTree) Print() {
	err := t.Walk(func(node *TreeNode, depth int) error {
		switch depth {
		case 0:
			id := node.Ref()
			if len(id) == 0 {
				id = node.Descriptor.Digest.String()
			}
			fmt.Printf("%s: %s, %d\n", node.Type, id, node.Descriptor.Size)
		case 1:
			fmt.Printf("%*s\n", 9, "|")
			fmt.Printf("%*s %s: %s, %d\n", 11, "|—>", node.Type, node.Descriptor.Digest.String(), node.Descriptor.Size)
		case 2:
			fmt.Printf("%*s\n", 9*depth, "|")
			(*ImageTree)(node).Print(depth)
			fmt.Println()
		}
		return nil
	})
	if err != nil {
		fmt.Printf("Failed to print image tree: %s\n", err.Error())
	}
}
