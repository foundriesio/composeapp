package compose

import "context"

type (
	AppStore interface {
		BlobProvider
		AddApps(appURIs []string) error
		ListApps(ctx context.Context) ([]*AppRef, error)
		RemoveApps(ctx context.Context, apps []*AppRef, prune bool) error
		Prune(ctx context.Context) ([]string, error)
	}
)
