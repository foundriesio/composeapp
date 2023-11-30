package compose

import "context"

type (
	AppStore interface {
		BlobProvider
		ListApps(ctx context.Context) ([]*AppRef, error)
	}
)
