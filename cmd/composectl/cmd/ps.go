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
		URI      string     `json:"uri"`
		Name     string     `json:"name"`
		State    string     `json:"state"`
		Services []*Service `json:"services"`
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
		Format string
	}
)

func init() {
	opts := psOptions{}
	psCmd.Flags().StringVar(&opts.Format, "format", "table", "Format the output. Values: [table | json]")
	psCmd.Run = func(cmd *cobra.Command, args []string) {
		if opts.Format != "table" && opts.Format != "json" {
			DieNotNil(fmt.Errorf("invalid value of `--format` option: %s", opts.Format))
		}
		psApps(cmd, args, &opts)
	}

	rootCmd.AddCommand(psCmd)
}

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "ps <ref> [<ref>]",
	Long:  ``,
}

func psApps(cmd *cobra.Command, args []string, opts *psOptions) {
	runningApps := getAllAppStatuses(cmd.Context())
	if len(args) == 0 {
		printAppStatuses(runningApps, opts.Format)
	} else {
		appStatuses := getAppsStatus(cmd.Context(), args, runningApps)
		printAppStatuses(appStatuses, opts.Format)
	}

}

func getAppsStatus(ctx context.Context, appRefs []string, runningApps map[string]*App) map[string]*App {
	apps := map[string]compose.App{}
	for _, appRef := range appRefs {
		app, _, err := v1.NewAppLoader().LoadAppTree(ctx,
			compose.NewStoreBlobProvider(path.Join(config.StoreRoot, "blobs", "sha256")),
			platforms.OnlyStrict(config.Platform), appRef)
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
			}
			continue
		}
		if len(runningApp.URI) > 0 && runningApp.URI != appRef {
			appStatuses[appRef] = &App{
				URI:   appRef,
				Name:  app.Name(),
				State: "running another version " + runningApp.URI,
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
			fmt.Printf("%s (%s) -> %s\n", app.Name, app.State, app.URI)
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

func getAppUri(storeApps []*compose.AppRef, appName string) string {
	var foundApp string
	for _, a := range storeApps {
		if a.Name == appName {
			// TODO: handle the case when there are more than two one version of the same app in the store
			foundApp = a.String()
		}
	}
	return foundApp
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
			appUri := getAppUri(storeApps, appName)
			if len(appUri) == 0 {
				appUri = "not found in store"
			}
			foundApps[appName] = &App{
				URI:      appUri,
				Name:     appName,
				State:    c.State,
				Services: []*Service{srv},
			}
		}
	}
	return foundApps
}
