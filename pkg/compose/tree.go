package compose

import (
	"fmt"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type (
	TreeNode struct {
		Descriptor *ocispec.Descriptor
		Type       BlobType
		Children   []*TreeNode
	}

	NodeProcessor func(node *TreeNode, depth int) error
)

const (
	MaxMerkleTreeDepth = 10
)

func (t *TreeNode) Walk(fn NodeProcessor) error {
	return t.walk(fn, 0)
}

func (t *TreeNode) walk(fn NodeProcessor, depth int) error {
	if depth > MaxMerkleTreeDepth {
		return fmt.Errorf("the maximum tree depth is reached; max depth: %d", MaxMerkleTreeDepth)
	}
	if err := fn(t, depth); err != nil {
		return err
	}
	for _, c := range t.Children {
		if err := c.walk(fn, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (t *TreeNode) NodeCount() (counter int) {
	err := t.Walk(func(node *TreeNode, depth int) error {
		counter++
		return nil
	})
	if err != nil {
		panic(err.Error())
	}
	return
}

func (t *TreeNode) HasRef() bool {
	return len(t.Descriptor.URLs) > 0
}

func (t *TreeNode) Ref() string {
	if t.HasRef() {
		return t.Descriptor.URLs[0]
	}
	return ""
}

func (t *TreeNode) GetServiceHash() string {
	switch t.Type {
	case BlobTypeImageIndex, BlobTypeSkopeoImageIndex, BlobTypeImageManifest:
	default:
		return ""
	}

	if t.Descriptor == nil {
		return ""
	}
	return t.Descriptor.Annotations[AppServiceHashLabelKey]
}
