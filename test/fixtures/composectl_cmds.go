package fixtures

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	"gopkg.in/yaml.v3"
	rand2 "math/rand"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"testing"
	"time"
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
		PublishLayersManifest bool
		PublishLayersMetaFile bool
		Registry              string
	}
)

func check(t *testing.T, err error) {
	if err != nil {
		t.Fatal(err)
	}
}

func checkf(t *testing.T, err error, format string, args ...any) {
	if err != nil {
		t.Fatalf(format, args...)
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
func WithLayersMeta(layersMetaFile bool) func(opts *PublishOpts) {
	return func(opts *PublishOpts) {
		opts.PublishLayersMetaFile = layersMetaFile
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
	check(t, os.MkdirAll(app.Dir, 0o755))
	check(t, os.WriteFile(path.Join(app.Dir, "docker-compose.yml"), []byte(composeDef), 0o640))
	return app
}

func (a *App) pullImages(t *testing.T) error {
	b, err := os.ReadFile(path.Join(a.Dir, "docker-compose.yml"))
	check(t, err)
	var composeProj map[string]interface{}
	check(t, yaml.Unmarshal(b, &composeProj))
	services := composeProj["services"]
	for _, v := range services.(map[string]interface{}) {
		image := v.(map[string]interface{})["image"]
		c := exec.Command("docker", "pull", image.(string))
		output, cmdErr := c.CombinedOutput()
		checkf(t, cmdErr, "failed to pull app images: %s\n", output)
	}
	return err
}

func (a *App) removeImages(t *testing.T) error {
	b, err := os.ReadFile(path.Join(a.Dir, "docker-compose.yml"))
	check(t, err)
	var composeProj map[string]interface{}
	check(t, yaml.Unmarshal(b, &composeProj))
	services := composeProj["services"]
	removedImages := map[string]bool{}
	for _, v := range services.(map[string]interface{}) {
		image := v.(map[string]interface{})["image"]
		if _, ok := removedImages[image.(string)]; ok {
			continue
		}
		c := exec.Command("docker", "image", "rm", image.(string))
		output, cmdErr := c.CombinedOutput()
		checkf(t, cmdErr, "failed to pull app images: %s\n", output)
		removedImages[image.(string)] = true
	}
	return err
}

func (a *App) Publish(t *testing.T, publishOpts ...func(*PublishOpts)) {
	check(t, a.pullImages(t))
	opts := PublishOpts{PublishLayersManifest: true, PublishLayersMetaFile: true}
	for _, o := range publishOpts {
		o(&opts)
	}
	if len(opts.Registry) == 0 {
		opts.Registry = "registry"
	}
	baseUri := opts.Registry + ":5000/factory/" + a.Name

	t.Run("publish app", func(t *testing.T) {
		digestFile := path.Join(a.Dir, "digest.sha256")
		tag, err := randomStringCrypto(7)
		check(t, err)
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
		runCmd(t, a.Dir, args...)
		b, err := os.ReadFile(digestFile)
		check(t, err)
		a.PublishedUri = baseUri + "@" + string(b)
		fmt.Printf("published app uri: %s\n", a.PublishedUri)
	})
	check(t, a.removeImages(t))
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
		homeDir, homeDirErr := os.UserHomeDir()
		check(t, homeDirErr)
		composeRoot := path.Join(homeDir, ".composeapps/projects", a.Name)

		c := exec.Command("docker", "compose", "up", "--remove-orphans", "-d")
		c.Dir = composeRoot
		output, err := c.CombinedOutput()
		checkf(t, err, "failed to run `docker compose up -d` command: %s\n", output)
	})
}

func (a *App) Stop(t *testing.T) {
	a.runCmd(t, "stop app", "stop", a.Name)
}

func (a *App) CheckFetched(t *testing.T) {
	t.Run("list app", func(t *testing.T) {
		output := runCmd(t, a.Dir, "ls", "--format", "json")
		var lsOutput []composectl.AppJsonOutput
		check(t, json.Unmarshal(output, &lsOutput))
		if a.PublishedUri != lsOutput[0].URI {
			t.Errorf("app uri in the list output does not equal to the published app;"+
				" published app uri: %s, app list uri: %s\n", a.PublishedUri, lsOutput[0].URI)
		}
	})
	t.Run("check app", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		check(t, json.Unmarshal(output, &checkResult))
		if len(checkResult.FetchCheck.MissingBlobs) > 0 {
			t.Errorf("There are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
		}
	})
}

func (a *App) CheckInstalled(t *testing.T) {
	t.Run("check if installed", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", "--install", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		check(t, json.Unmarshal(output, &checkResult))
		if len(checkResult.FetchCheck.MissingBlobs) > 0 {
			t.Errorf("there are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
		}
		if checkResult.InstallCheck == nil || len(*checkResult.InstallCheck) != 1 {
			t.Errorf("invalid install check result: %+v\n", checkResult.InstallCheck)
		}
		installCheckRes, ok := (*checkResult.InstallCheck)[a.PublishedUri]
		if !ok {
			t.Errorf("no app in the install check result: %+v\n", *checkResult.InstallCheck)
		}
		if len(installCheckRes.MissingImages) > 0 {
			t.Errorf("there are missing app images in docker store: %+v\n", installCheckRes.MissingImages)
		}
	})
}

func (a *App) CheckRunning(t *testing.T) {
	t.Run("check if running", func(t *testing.T) {
		output := runCmd(t, "", "ps", a.PublishedUri, "--format", "json")
		var psOutput map[string]composectl.App
		check(t, json.Unmarshal(output, &psOutput))
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

func (a *App) runCmd(t *testing.T, desc string, args ...string) {
	t.Run(desc, func(t *testing.T) {
		runCmd(t, a.Dir, args...)
	})
}

func runCmd(t *testing.T, appDir string, args ...string) []byte {
	c := exec.Command(composeExec, args...)
	if len(appDir) > 0 {
		c.Dir = appDir
	}
	output, err := c.CombinedOutput()
	checkf(t, err, "failed to run `%s` command: %s\n", args[0], output)
	return output
}

func randomStringCrypto(length int) (string, error) {
	bytes := make([]byte, length)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz"
	seededRand := rand2.New(rand2.NewSource(time.Now().UnixNano()))
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}
