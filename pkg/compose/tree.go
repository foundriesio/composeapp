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
