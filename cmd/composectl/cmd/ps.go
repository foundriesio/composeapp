package composectl

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/docker/api/types/container"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"github.com/spf13/cobra"
	"path"
)

const (
	WorkingDirLabel = "com.docker.compose.project.working_dir"
	ServiceLabel    = "com.docker.compose.service"
)

type (
	Service struct {
		Name   string `json:"name"`
		Image  string `json:"image"`
		Hash   string `json:"hash"`
		CtrID  string `json:"ctr-id"`
		State  string `json:"state"`
		Status string `json:"status"`
		Health string `json:"health,omitempty"`
	}
	App struct {
		URI           string                `json:"uri"`
		Name          string                `json:"name"`
		State         string                `json:"state"`
		Services      []*Service            `json:"services"`
		InStore       bool                  `json:"in_store"`
		BundleErrors  compose.AppBundleErrs `json:"bundle_errors"`
		MissingImages []string              `json:"missing_images"`
	}
	ServiceStatus struct {
		URI     string `json:"uri"`
		ID      string `json:"id"`
		CfgHash string `json:"cfg-hash"`
		State   string `json:"state"`
		Status  string `json:"status"`
	}
	AppStatus []ServiceStatus

	psOptions struct {
		Format       string
		CheckInstall bool
	}
)

func init() {
	opts := psOptions{}
	psCmd.Flags().StringVar(&opts.Format, "format", "table", "Format the output. Values: [table | json]")
	psCmd.Flags().BoolVar(&opts.CheckInstall, "install", true, "Also check if app is installed")
	psCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "table" && opts.Format != "json" {
			DieNotNil(fmt.Errorf("invalid value of `--format` option: %s", opts.Format))
		}
		psApps(cmd, args, &opts)
	}

	rootCmd.AddCommand(psCmd)
}

var psCmd = &cobra.Command{
	Use:   "ps [<ref>]...",
	Short: "ps [<ref>]...",
	Long:  ``,
}

func psApps(cmd *cobra.Command, args []string, opts *psOptions) {
	runningApps := getAllAppStatuses(cmd.Context())
	if len(args) == 0 {
		printAppStatuses(runningApps, opts.Format)
	} else {
		appStatuses := getAppsStatus(cmd.Context(), args, runningApps, opts.CheckInstall)
		printAppStatuses(appStatuses, opts.Format)
	}

}

func getAppsStatus(ctx context.Context, appRefs []string, runningApps map[string]*App, checkInstall bool) map[string]*App {
	store, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	apps := map[string]compose.App{}
	for _, appRef := range appRefs {
		app, err := v1.NewAppLoader().LoadAppTree(ctx, store, platforms.OnlyStrict(config.Platform), appRef)
		DieNotNil(err)
		apps[appRef] = app
	}

	appStatuses := map[string]*App{}
	for appRef, app := range apps {
		runningApp, ok := runningApps[app.Name()]
		if !ok {
			appStatuses[appRef] = &App{
				URI:   appRef,
				Name:  app.Name(),
				State: "not running",
				// app is present in the store because its tree was loaded successfully prior to executing this check
				InStore: true,
			}
			continue
		}
		if !runningApp.InStore {
			// Since we iterate over apps stored in apps, it is not possible that running app is not in store
			// in this context/loop.
			fmt.Printf("ERR: Running app is not found in the store: %s\n", appRef)
			continue
		}
		// If the running app URI is empty and the app is found in the store then it
		// means that more than one version of the app are in the store.
		// In this case we assume that a caller/user would like to check status of the app version
		// specified via `appRefs`.
		if len(runningApp.URI) > 0 && runningApp.URI != appRef {
			appStatuses[appRef] = &App{
				URI:   appRef,
				Name:  app.Name(),
				State: "running another app version than found in the store" + runningApp.URI,
			}
			continue
		}

		composeTree := app.GetComposeRoot()
		if composeTree == nil {
			panic(fmt.Errorf("failed to get app tree for %s", app.Name()))
		}
		var appServices []*Service
		appState := "running"
		for _, imageNode := range composeTree.Children {
			imageUri := imageNode.Descriptor.URLs[0]

			var foundSrvs []*Service
			for _, srv := range runningApp.Services {
				if srv.Image == imageUri {
					foundSrvs = append(foundSrvs, srv)
				}
			}
			if len(foundSrvs) == 0 {
				appServices = append(appServices, &Service{
					Image: imageUri,
					State: "missing",
				})
				appState = "not running"
				continue
			}
			appServiceHash := imageNode.Descriptor.Annotations[v1.AppServiceHashLabelKey]
			var foundMatchingSrv *Service
			for _, fsrv := range foundSrvs {
				if len(fsrv.Hash) == 0 {
					appServices = append(appServices, &Service{
						Image:  imageUri,
						State:  "unknown",
						Status: "no config hash",
					})
					appState = "unknown"
					continue
				}
				if fsrv.Hash == appServiceHash {
					foundMatchingSrv = fsrv
					break
				}
			}
			if foundMatchingSrv != nil {
				appServices = append(appServices, foundMatchingSrv)
				if len(foundMatchingSrv.State) == 0 || foundMatchingSrv.State != "running" {
					appState = "not running"
				}
			} else {
				appServices = append(appServices, &Service{
					Image:  imageUri,
					State:  "missing",
					Status: "config hash mismatch",
				})
				appState = "not running"
			}
		}

		appStatuses[appRef] = &App{
			URI:      appRef,
			Name:     app.Name(),
			State:    appState,
			Services: appServices,
			InStore:  true,
		}
	}

	if checkInstall {
		checkInstallResult, err := checkIfInstalled(ctx, appRefs, store, config.DockerHost)
		DieNotNil(err)
		for app, ir := range checkInstallResult {
			appStatuses[app].BundleErrors = ir.BundleErrors
			appStatuses[app].MissingImages = ir.MissingImages
			if appStatuses[app].State == "running" && len(ir.BundleErrors) > 0 {
				appStatuses[app].State = "running with an invalid app bundle"
			}
		}
	}
	return appStatuses
}

func printAppStatuses(appStatuses map[string]*App, format string) {
	if format == "json" {
		if b, err := json.MarshalIndent(appStatuses, "", "  "); err == nil {
			fmt.Println(string(b))
		} else {
			DieNotNil(err)
		}
	} else {
		for _, app := range appStatuses {
			var appUri string
			if app.InStore {
				if len(app.URI) > 0 {
					appUri = app.URI
				} else {
					appUri = "multiple versions of app found in the app store"
				}
			} else {
				appUri = "not found in the app store"
			}
			fmt.Printf("%s (%s) -> %s\n", app.Name, app.State, appUri)
			for _, srv := range app.Services {
				id := "------------"
				if len(srv.CtrID) > 0 {
					id = srv.CtrID[:12]
				}
				hash := "------------"
				if len(srv.Hash) > 0 {
					hash = srv.Hash[:12]
				}
				fmt.Printf("  - %s\t%s\t%s\t%s\t%s\t%s\n", srv.Name, srv.Image, id, hash, srv.State, srv.Status)
			}
		}
	}
}

func checkAppInStore(storeApps []*compose.AppRef, appName string) []*compose.AppRef {
	var foundApps []*compose.AppRef
	for _, a := range storeApps {
		if a.Name == appName {
			// TODO: handle the case when there are more than two one version of the same app in the store
			foundApps = append(foundApps, a)
		}
	}
	return foundApps
}
func getAllAppStatuses(ctx context.Context) map[string]*App {
	store, err := v1.NewAppStore(config.StoreRoot, config.Platform)
	DieNotNil(err)
	storeApps, err := store.ListApps(ctx)
	DieNotNil(err)
	dockerClient, err := compose.GetDockerClient(config.DockerHost)
	DieNotNil(err)
	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	DieNotNil(err)

	foundApps := map[string]*App{}
	for _, c := range containers {
		// check if container is part of a compose project
		var workDir string
		var foundProjectWorkDir bool
		if workDir, foundProjectWorkDir = c.Labels[WorkingDirLabel]; !foundProjectWorkDir {
			fmt.Printf("container is not part of any compose app; ID: %s, image: %s\n", c.ID, c.Image)
			continue
		}
		var health string
		if cInfo, err := dockerClient.ContainerInspect(ctx, c.ID); err == nil {
			if cInfo.State.Health != nil {
				health = cInfo.State.Health.Status
			}
		}

		appName := path.Base(workDir)
		srv := &Service{
			Name:   c.Labels[ServiceLabel],
			Image:  c.Image,
			Hash:   c.Labels[v1.AppServiceHashLabelKey],
			CtrID:  c.ID,
			State:  c.State,
			Status: c.Status,
			Health: health,
		}
		if app, ok := foundApps[appName]; ok {
			app.Services = append(app.Services, srv)
			if c.State != "running" {
				app.State = "not running"
			}
		} else {
			var appUri string
			appsFoundInStore := checkAppInStore(storeApps, appName)
			if len(appsFoundInStore) == 1 {
				appUri = appsFoundInStore[0].String()
			}
			foundApps[appName] = &App{
				URI:      appUri,
				Name:     appName,
				State:    c.State,
				Services: []*Service{srv},
				InStore:  len(appsFoundInStore) > 0,
			}
		}
	}
	return foundApps
}
