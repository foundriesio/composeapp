package e2e_tests

import (
	"context"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"path"
	"testing"
)

func check(t *testing.T, err error) {
	if err != nil {
		t.Fatal(err)
	}
}

func TestAppUpdate(t *testing.T) {
	appComposeDef := `
services:
  srvs-01:
    image: registry:5000/factory/runner-image:v0.1
    command: sh -c "while true; do sleep 60; done"
    ports:
    - 8080:80
`
	app := f.NewApp(t, appComposeDef)
	app.Publish(t)

	cfg, err := v1.NewDefaultConfig()
	check(t, err)

	cfg.StoreRoot = f.AppStoreRoot
	cfg.DBFilePath = path.Join(cfg.StoreRoot, "updates.db")

	updateRunner, err := update.NewUpdate(cfg, "target-1")
	check(t, err)

	ctx := context.Background()

	check(t, updateRunner.Init(ctx, []string{app.PublishedUri}))
	updateStatus := updateRunner.Status()
	if updateStatus.State != update.StateInitialized {
		t.Fatal("update not initialized")
	}
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not initiated for 100%%: %d\n", updateStatus.Progress)
	}

	check(t, updateRunner.Fetch(ctx))
	defer app.Remove(t)
	updateStatus = updateRunner.Status()
	if updateStatus.State != update.StateFetched {
		t.Fatal("update not fetched")
	}
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not fetched for 100%%: %d\n", updateStatus.Progress)
	}

	check(t, updateRunner.Install(ctx))
	defer app.Uninstall(t)
	updateStatus = updateRunner.Status()
	if updateStatus.State != update.StateInstalled {
		t.Fatal("update not installed")
	}
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not installed for 100%%: %d\n", updateStatus.Progress)
	}

	check(t, updateRunner.Start(ctx))
	defer app.Stop(t)
	if updateRunner.Status().State != update.StateStarted {
		t.Fatal("update not started")
	}
	updateStatus = updateRunner.Status()
	if updateStatus.Progress != 100 {
		t.Fatalf("update is not started for 100%%: %d\n", updateStatus.Progress)
	}

	defer func() {
		switch updateRunner.Status().State {
		case update.StateInitializing,
			update.StateInitialized,
			update.StateFetching,
			update.StateFetched,
			update.StateInstalled,
			update.StateInstalling:
			check(t, updateRunner.Cancel(ctx))
			if updateRunner.Status().State != update.StateCanceled {
				t.Fatalf("update not cancelled: %s\n", updateRunner.Status().State)
			}
		case update.StateStarting,
			update.StateStarted:
			check(t, updateRunner.Complete(ctx))
			if updateRunner.Status().State != update.StateCompleted {
				t.Fatalf("update not completed: %s\n", updateRunner.Status().State)
			}
		}
	}()
}
