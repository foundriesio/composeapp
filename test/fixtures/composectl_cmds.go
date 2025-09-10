package fixtures

import (
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/images"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/ocischema"
	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	"github.com/foundriesio/composeapp/pkg/compose"
	v1 "github.com/foundriesio/composeapp/pkg/compose/v1"
	"gopkg.in/yaml.v3"
	"io"
	rand2 "math/rand"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"testing"
	"time"
)

const (
	AppStoreRoot       = "/var/sota/reset-apps"
	AppComposeRootRoot = "/var/sota/compose-apps"
)

var (
	composeExec = os.Getenv("COMPOSECTL_EXE")
)

type (
	App struct {
		Name         string
		PublishedUri string
		Dir          string
	}

	PublishOpts struct {
		PublishLayersManifest   bool
		PublishLayersMetaFile   bool
		Registry                string
		PublishAppBundleIndexes bool
	}
)

func NewTestConfig(t *testing.T) *compose.Config {
	cfg, err := v1.NewDefaultConfig(v1.WithStoreRoot(AppStoreRoot), v1.WithComposeRoot(AppComposeRootRoot))
	Check(t, err)
	return cfg
}

func Check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func Checkf(t *testing.T, err error, format string, args ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf(format, args...)
	}
}

func WithAppBundleIndexes(enableAppBundleIndexes bool) func(opts *PublishOpts) {
	return func(opts *PublishOpts) {
		opts.PublishAppBundleIndexes = enableAppBundleIndexes
	}
}

func WithRegistry(registry string) func(opts *PublishOpts) {
	return func(opts *PublishOpts) {
		opts.Registry = registry
	}
}
func WithLayersManifest(addLayerManifest bool) func(opts *PublishOpts) {
	return func(opts *PublishOpts) {
		opts.PublishLayersManifest = addLayerManifest
	}
}

func NewApp(t *testing.T, composeDef string, name ...string) *App {
	app := &App{}
	if len(name) > 0 {
		app.Name = name[0]
	} else {
		app.Name = randomString(5)
	}
	app.Dir = path.Join(t.TempDir(), app.Name)
	Check(t, os.MkdirAll(app.Dir, 0o755))
	Check(t, os.WriteFile(path.Join(app.Dir, "docker-compose.yml"), []byte(composeDef), 0o640))
	return app
}

func (a *App) GetAppImages(t *testing.T) []string {
	return a.getAppImages(t, path.Join(a.Dir, "docker-compose.yml"))
}

func (a *App) GetAppImagesFromAppStore(t *testing.T) []string {
	appRef, err := compose.ParseAppRef(a.PublishedUri)
	Check(t, err)
	appRoot := path.Join(AppStoreRoot, "apps", a.Name, appRef.Digest.Encoded())
	composeFilePath := path.Join(appRoot, "docker-compose.yml")
	return a.getAppImages(t, composeFilePath)
}

func (a *App) getAppImages(t *testing.T, composeFilePath string) []string {
	b, err := os.ReadFile(composeFilePath)
	Check(t, err)

	var composeProj map[string]interface{}
	Check(t, yaml.Unmarshal(b, &composeProj))
	services := composeProj["services"]
	var images []string
	for _, v := range services.(map[string]interface{}) {
		image := v.(map[string]interface{})["image"]
		images = append(images, image.(string))
	}
	return images
}

func (a *App) pullImages(t *testing.T) {
	images := a.GetAppImages(t)
	for _, image := range images {
		c := exec.Command("docker", "pull", image)
		output, cmdErr := c.CombinedOutput()
		Checkf(t, cmdErr, "failed to pull app images: %s\n", output)
	}
}

func (a *App) removeImages(t *testing.T) {
	images := a.GetAppImages(t)
	removedImages := map[string]bool{}
	for _, image := range images {
		if _, ok := removedImages[image]; ok {
			continue
		}
		c := exec.Command("docker", "image", "rm", image)
		output, cmdErr := c.CombinedOutput()
		Checkf(t, cmdErr, "failed to pull app images: %s\n", output)
		removedImages[image] = true
	}
}

func (a *App) Publish(t *testing.T, publishOpts ...func(*PublishOpts)) {
	a.pullImages(t)
	opts := PublishOpts{PublishLayersManifest: true, PublishLayersMetaFile: true, PublishAppBundleIndexes: true}
	for _, o := range publishOpts {
		o(&opts)
	}
	if len(opts.Registry) == 0 {
		opts.Registry = "registry"
	}
	baseUri := opts.Registry + ":5000/factory/" + a.Name

	t.Run("publish app", func(t *testing.T) {
		digestFile := path.Join(a.Dir, "digest.sha256")
		tag := randomString(7)
		args := []string{
			"publish", "-d", digestFile, baseUri + ":" + tag, "amd64",
		}
		if !opts.PublishLayersManifest {
			args = append(args, "--layers-manifest=false")
		}
		if opts.PublishLayersMetaFile {
			layersMetaFile := GenerateLayersMetaFile(t, filepath.Dir(a.Dir))
			defer os.RemoveAll(layersMetaFile)
			args = append(args, "--layers-meta", layersMetaFile)
		}
		if !opts.PublishAppBundleIndexes {
			os.Setenv("APP_BUNDLE_INDEX_OFF", "1")
		}
		defer func() {
			if !opts.PublishAppBundleIndexes {
				os.Unsetenv("APP_BUNDLE_INDEX_OFF")
			}
		}()
		runCmd(t, a.Dir, args...)
		b, err := os.ReadFile(digestFile)
		Check(t, err)
		a.PublishedUri = baseUri + "@" + string(b)
		fmt.Printf("published app uri: %s\n", a.PublishedUri)
	})
	a.removeImages(t)
}

func (a *App) Pull(t *testing.T) {
	a.runCmd(t, "pull app", "pull", a.PublishedUri, "-u", "90")
}

func (a *App) Remove(t *testing.T) {
	a.runCmd(t, "remove app", "rm", a.PublishedUri)
}

func (a *App) Install(t *testing.T) {
	a.runCmd(t, "install app", "install", a.PublishedUri)
}

func (a *App) Uninstall(t *testing.T) {
	a.runCmd(t, "uninstall app", "uninstall", "--prune=true", a.Name)
}

func (a *App) Run(t *testing.T) {
	a.runCmd(t, "run app", "run", a.Name)
}

func (a *App) Up(t *testing.T) {
	t.Run("compose up", func(t *testing.T) {
		composeRoot := path.Join(AppComposeRootRoot, a.Name)

		c := exec.Command("docker", "compose", "up", "--remove-orphans", "-d")
		c.Dir = composeRoot
		output, err := c.CombinedOutput()
		Checkf(t, err, "failed to run `docker compose up -d` command: %s\n", output)
	})
}

func (a *App) Stop(t *testing.T) {
	a.runCmd(t, "stop app", "stop", a.Name)
}

func (a *App) CheckFetched(t *testing.T) {
	t.Run("list app", func(t *testing.T) {
		output := runCmd(t, a.Dir, "ls", "--format", "json")
		var lsOutput []composectl.AppJsonOutput
		Check(t, json.Unmarshal(output, &lsOutput))
		if a.PublishedUri != lsOutput[0].URI {
			t.Errorf("app uri in the list output does not equal to the published app;"+
				" published app uri: %s, app list uri: %s\n", a.PublishedUri, lsOutput[0].URI)
		}
	})
	t.Run("Check app", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		Check(t, json.Unmarshal(output, &checkResult))
		if len(checkResult.FetchCheck.MissingBlobs) > 0 {
			t.Errorf("There are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
		}
	})
}

func (a *App) CheckInstalled(t *testing.T) {
	t.Run("Check if installed", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", "--install", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		Check(t, json.Unmarshal(output, &checkResult))
		if len(checkResult.FetchCheck.MissingBlobs) > 0 {
			t.Errorf("there are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
		}
		if checkResult.InstallCheck == nil || len(*checkResult.InstallCheck) != 1 {
			t.Errorf("invalid install Check result: %+v\n", checkResult.InstallCheck)
		}
		installCheckRes, ok := (*checkResult.InstallCheck)[a.PublishedUri]
		if !ok {
			t.Errorf("no app in the install Check result: %+v\n", *checkResult.InstallCheck)
		}
		if len(installCheckRes.MissingImages) > 0 {
			t.Errorf("there are missing app images in docker store: %+v\n", installCheckRes.MissingImages)
		}
	})
}

func (a *App) GetInstallCheckRes(t *testing.T) (checkRes *composectl.AppInstallCheckResult) {
	t.Run("Check if installed", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", "--install", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		Check(t, json.Unmarshal(output, &checkResult))
		if len(checkResult.FetchCheck.MissingBlobs) > 0 {
			t.Errorf("there are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
		}
		if checkResult.InstallCheck == nil || len(*checkResult.InstallCheck) != 1 {
			t.Errorf("invalid install Check result: %+v\n", checkResult.InstallCheck)
		}
		var ok bool
		checkRes, ok = (*checkResult.InstallCheck)[a.PublishedUri]
		if !ok {
			t.Errorf("no app in the install Check result: %+v\n", *checkResult.InstallCheck)
		}
	})
	return
}

func (a *App) CheckRunning(t *testing.T) {
	t.Run("Check if running", func(t *testing.T) {
		output := runCmd(t, "", "ps", a.PublishedUri, "--format", "json")
		var psOutput map[string]composectl.App
		Check(t, json.Unmarshal(output, &psOutput))
		if len(psOutput) != 1 {
			t.Errorf("expected one element in ps output, got: %d\n", len(psOutput))
		}
		appStatus, ok := psOutput[a.PublishedUri]
		if !ok {
			t.Errorf("no app URI in the ps output: %+v\n", psOutput)
		}
		if appStatus.State != "running" {
			t.Errorf("app is not running, its state: %+s\n", appStatus.State)
		}
	})
}

func (a *App) GetRunningStatus(t *testing.T) (appStatus *composectl.App) {
	t.Run("Check if running", func(t *testing.T) {
		output := runCmd(t, "", "ps", a.PublishedUri, "--format", "json")
		var psOutput map[string]composectl.App
		Check(t, json.Unmarshal(output, &psOutput))
		if len(psOutput) != 1 {
			t.Errorf("expected one element in ps output, got: %d\n", len(psOutput))
		}
		if appStatusRes, ok := psOutput[a.PublishedUri]; ok {
			appStatus = &appStatusRes
		} else {
			t.Errorf("no app URI in the ps output: %+v\n", psOutput)
		}
	})
	return
}

func (a *App) PullAppImagesWithSkopeo(t *testing.T) {
	storeRoot := AppStoreRoot
	blobsRoot := path.Join(storeRoot, "blobs")
	appRef, err := compose.ParseAppRef(a.PublishedUri)
	Check(t, err)
	appRoot := path.Join(storeRoot, "apps", a.Name, appRef.Digest.Encoded())
	imagesRoot := path.Join(storeRoot, "apps", a.Name, appRef.Digest.Encoded(), "images")

	// Download app manifest
	Check(t, os.MkdirAll(appRoot, 0x777))
	manifestUri := "https://" + appRef.Spec.Hostname() + "/v2/" + appRef.Repo + "/" + appRef.Name +
		"/manifests/sha256:" + appRef.Digest.Encoded()
	r, err := http.NewRequest("GET", manifestUri, nil)
	Check(t, err)
	r.Header = map[string][]string{"accept": {"application/vnd.oci.image.manifest.v1+json"}}
	resp, err := http.DefaultClient.Do(r)
	Check(t, err)
	mb, err := io.ReadAll(resp.Body)
	Check(t, err)
	Check(t, os.WriteFile(path.Join(appRoot, "manifest.json"), mb, 0x644))
	var appManifest ocischema.Manifest
	Check(t, json.Unmarshal(mb, &appManifest))

	// Download app bundle
	appBundlerHash := appManifest.Layers[0].Digest.Hex()
	appBundleUri := "https://" + appRef.Spec.Hostname() + "/v2/" + appRef.Repo + "/" + appRef.Name +
		"/blobs/sha256:" + appBundlerHash
	resp, err = http.Get(appBundleUri)
	Check(t, err)
	bb, err := io.ReadAll(resp.Body)
	Check(t, err)
	Check(t, os.WriteFile(path.Join(appRoot, appBundlerHash+".tgz"), bb, 0x644))

	// Extract docker-compose.yml from the app bundle archive and write it to the app directory
	c := exec.Command("tar", "-xzf", appBundlerHash+".tgz", "docker-compose.yml")
	c.Dir = appRoot
	output, err := c.CombinedOutput()
	Checkf(t, err, "failed to run tar command: %s\n", output)

	// write the app uri into the `uri` file
	Check(t, os.WriteFile(path.Join(appRoot, "uri"), []byte(a.PublishedUri), 0x644))

	// read the app compose project and pull its images by using `skopeo`
	b, err := os.ReadFile(path.Join(appRoot, "docker-compose.yml"))
	Check(t, err)
	var composeProj map[string]interface{}
	Check(t, yaml.Unmarshal(b, &composeProj))
	services := composeProj["services"]

	for _, v := range services.(map[string]interface{}) {
		image := (v.(map[string]interface{})["image"]).(string)
		imageRef, err := compose.ParseImageRef(image)
		Check(t, err)
		imageDir := path.Join(imagesRoot, imageRef.Locator, imageRef.Digest.Encoded())
		Check(t, os.MkdirAll(imageDir, 0x777))
		c := exec.Command("skopeo", "copy", "--insecure-policy", "-f", "v2s2", "--dest-shared-blob-dir",
			blobsRoot, "docker://"+image, "oci:.")
		c.Dir = imageDir
		output, cmdErr := c.CombinedOutput()
		Checkf(t, cmdErr, "failed to pull app images: %s; %s\n", cmdErr, output)
	}
}

func (a *App) GetAppImageManifest(t *testing.T, image string) (imageManifest ocischema.Manifest) {
	imageRef, err := compose.ParseImageRef(image)
	Check(t, err)
	manifestPath := path.Join(compose.GetBlobsRootFor(AppStoreRoot), imageRef.Digest.Encoded())

	var b []byte
	b, err = os.ReadFile(manifestPath)
	Check(t, err)
	var imageRoot compose.ImageRoot
	Check(t, json.Unmarshal(b, &imageRoot))

	if images.IsManifestType(imageRoot.MediaType) {
		if len(imageRoot.Manifests) != 0 || images.IsIndexType(imageRoot.MediaType) {
			t.Fatal("image media type: expected manifest but found index")
		}
	} else if images.IsIndexType(imageRoot.MediaType) {
		if len(imageRoot.Config) != 0 || len(imageRoot.Layers) != 0 {
			t.Fatal("image media type: expected index but found manifest")
		}
		var imageManifestList manifestlist.ManifestList
		Check(t, json.Unmarshal(b, &imageManifestList))
		for _, manifestDescriptor := range imageManifestList.Manifests {
			if manifestDescriptor.Platform.Architecture == "amd64" {
				manifestPath = path.Join(compose.GetBlobsRootFor(AppStoreRoot), manifestDescriptor.Digest.Encoded())
				b, err = os.ReadFile(manifestPath)
				Check(t, err)
				break
			}
		}
	} else {
		t.Fatalf("unknown image media type: %s", imageRoot.MediaType)
	}

	Check(t, json.Unmarshal(b, &imageManifest))
	return
}

func (a *App) runCmd(t *testing.T, desc string, args ...string) {
	t.Helper()
	t.Run(desc, func(t *testing.T) {
		t.Helper()
		runCmd(t, a.Dir, args...)
	})
}

func runCmd(t *testing.T, appDir string, args ...string) []byte {
	t.Helper()
	c := exec.Command(composeExec, args...)
	if len(appDir) > 0 {
		c.Dir = appDir
	}
	output, err := c.CombinedOutput()
	Checkf(t, err, "failed to run `%s` command: %s\n", args[0], output)
	return output
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	seededRand := rand2.New(rand2.NewSource(time.Now().UnixNano()))
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}
