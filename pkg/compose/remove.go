package compose

import (
	"context"
	"fmt"
)

type (
	RemoveOpts struct {
		Prune       bool
		CheckStatus bool
	}
	RemoveOpt func(*RemoveOpts)
)

func WithoutBlobPruning() RemoveOpt {
	return func(opts *RemoveOpts) {
		opts.Prune = false
	}
}
func WithoutCheckStatus() RemoveOpt {
	return func(opts *RemoveOpts) {
		opts.CheckStatus = false
	}
}

func RemoveApps(ctx context.Context, cfg *Config, appRefs []string, options ...RemoveOpt) error {
	if len(appRefs) == 0 {
		return nil
	}
	opts := &RemoveOpts{Prune: true, CheckStatus: true}
	for _, o := range options {
		o(opts)
	}
	if opts.CheckStatus {
		appsStatus, err := CheckAppsStatus(ctx, cfg, appRefs)
		if err != nil {
			return err
		}
		if appsStatus.AreRunning() {
			return fmt.Errorf("cannot remove running apps; stop and uinstall them before removing")
		}
		if appsStatus.AreInstalled() {
			return fmt.Errorf("cannot remove installed apps; uinstall them before removing")
		}
		if !appsStatus.AreFetched() {
			return fmt.Errorf("cannot remove not full fetched apps; run the 'prune' to remove unused blobs")
		}
	}
	store, err := cfg.AppStoreFactory(cfg)
	if err != nil {
		return err
	}
	var refs []*AppRef
	for _, uri := range appRefs {
		if ref, err := ParseAppRef(uri); err == nil {
			refs = append(refs, ref)
		} else {
			return err
		}
	}
	return store.RemoveApps(ctx, refs, opts.Prune)
}
