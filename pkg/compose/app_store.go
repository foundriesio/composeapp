package compose

import "context"

type (
	AppStore interface {
		BlobProvider
		ListApps(ctx context.Context) ([]*AppRef, error)
		RemoveApps(ctx context.Context, apps []*AppRef, prune bool) error
		Prune(ctx context.Context) ([]string, error)
	}
)
