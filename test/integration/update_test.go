package e2e_tests

import (
	"context"
	"github.com/foundriesio/composeapp/pkg/compose"
	"github.com/foundriesio/composeapp/pkg/update"
	f "github.com/foundriesio/composeapp/test/fixtures"
	"testing"
)

func check(t *testing.T, err error) {
	if err != nil {
		t.Fatal(err)
	}
}

func finalizeUpdate(t *testing.T, ctx context.Context, ur update.Runner) {
	switch ur.Status().State {
	case update.StateInitializing,
		update.StateInitialized,
		update.StateFetching,
		update.StateFetched,
		update.StateInstalled,
		update.StateInstalling:
		check(t, ur.Cancel(ctx))
		if ur.Status().State != update.StateCanceled {
			t.Fatalf("update not cancelled: %s\n", ur.Status().State)
		}
	case update.StateStarting,
		update.StateStarted:
		check(t, ur.Complete(ctx))
		if ur.Status().State != update.StateCompleted {
			t.Fatalf("update not completed: %s\n", ur.Status().State)
		}
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

	cfg := f.NewTestConfig(t)
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

	defer finalizeUpdate(t, ctx, updateRunner)
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

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	s, err := compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	check(t, err)
	if s.AreFetched() || s.AreInstalled() || s.AreRunning() {
		t.Fatalf("apps are not supposed to be fetched nor installed nor running")
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
	s, err = compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	check(t, err)
	if !s.AreFetched() {
		t.Fatalf("apps are supposed to be fetched")
	}
	if s.AreInstalled() || s.AreRunning() {
		t.Fatalf("apps are not suppoped to be installed nor running")
	}

	check(t, updateRunner.Install(ctx))
	defer app.Uninstall(t)
	if updateRunner.Status().State != update.StateInstalled {
		t.Fatal("update not installed")
	}
	s, err = compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	check(t, err)
	if !(s.AreFetched() && s.AreInstalled()) {
		t.Fatalf("apps are supposed to be fetched and installed")
	}
	if s.AreRunning() {
		t.Fatalf("apps are not suppoped to be installed nor running")
	}

	check(t, updateRunner.Start(ctx))
	defer app.Stop(t)
	if updateRunner.Status().State != update.StateStarted {
		t.Fatal("update not started")
	}

	s, err = compose.CheckAppsStatus(ctx, cfg, []string{app.PublishedUri})
	check(t, err)
	if !(s.AreFetched() && s.AreInstalled() && s.AreRunning()) {
		t.Fatalf("apps are supposed to be fetched and installed and running")
	}

	defer finalizeUpdate(t, ctx, updateRunner)
}

func TestAppControl(t *testing.T) {
	appComposeDef01 := `
services:
 srvs-01:
   image: registry:5000/factory/runner-image:v0.1
   command: sh -c "while true; do sleep 60; done"
   ports:
   - 8080:80
`
	appComposeDef02 := `
services:
  busybox:
    image: ghcr.io/foundriesio/busybox:1.36
    command: sh -c "while true; do sleep 60; done"
`
	var appURIs []string
	for _, appDef := range []string{appComposeDef01, appComposeDef02} {
		app := f.NewApp(t, appDef)
		app.Publish(t)
		appURIs = append(appURIs, app.PublishedUri)
	}

	cfg := f.NewTestConfig(t)
	ctx := context.Background()

	updateRunner, err := update.NewUpdate(cfg, "target-2")
	check(t, err)

	check(t, updateRunner.Init(ctx, appURIs))
	check(t, updateRunner.Fetch(ctx))
	defer func() {
		defer func() {
			check(t, compose.RemoveApps(ctx, cfg, appURIs))
		}()
	}()

	check(t, updateRunner.Install(ctx))
	defer func() {
		check(t, compose.UninstallApps(ctx, cfg, appURIs))
	}()

	check(t, updateRunner.Start(ctx))
	defer func() {
		check(t, compose.StopApps(ctx, cfg, appURIs))
	}()

	defer finalizeUpdate(t, ctx, updateRunner)
}
