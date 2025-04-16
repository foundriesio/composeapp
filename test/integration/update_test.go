package e2e_tests

import (
	"context"
	"errors"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/foundriesio/composeapp/pkg/update"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"path"
	"strings"
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

func TestAppSync(t *testing.T) {
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

	ctx := context.Background()

	// App is not fetched is not installed is not running
	err = compose.CheckRunning(ctx, cfg, []string{app.PublishedUri})
	if !errors.Is(err, compose.ErrAppNotFound) {
		t.Fatal("unexpected error: ", err)
	}

	updateRunner, err := update.NewUpdate(cfg, "target-1")
	check(t, err)

	check(t, updateRunner.Init(ctx, []string{app.PublishedUri}))
	if updateRunner.Status().State != update.StateInitialized {
		t.Fatal("update not initialized")
	}

	check(t, updateRunner.Fetch(ctx))
	defer app.Remove(t)

	// App is fetched but is not installed and is not running
	err = compose.CheckRunning(ctx, cfg, []string{app.PublishedUri})
	if composeInstallErr, ok := err.(*compose.ErrComposeInstall); ok {
		for f, errMsg := range composeInstallErr.Errs {
			if f != "docker-compose.yml" {
				t.Fatalf("unexpected error file in the compose project: %s\n", f)
			}
			if !strings.Contains(errMsg, "no such file or directory") {
				t.Fatalf("unexpected error in the compose project: %s\n", errMsg)
			}
		}
	} else {
		t.Fatal("unexpected error: ", err)
	}

	check(t, updateRunner.Install(ctx))
	defer app.Uninstall(t)
	if updateRunner.Status().State != update.StateInstalled {
		t.Fatal("update not installed")
	}
	//err = compose.CheckRunning(ctx, cfg, []string{app.PublishedUri})
	//check(t, err)

	check(t, updateRunner.Start(ctx))
	defer app.Stop(t)
	if updateRunner.Status().State != update.StateStarted {
		t.Fatal("update not started")
	}

	err = compose.CheckRunning(ctx, cfg, []string{app.PublishedUri})
	check(t, err)

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
