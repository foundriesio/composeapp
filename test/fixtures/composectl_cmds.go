package fixtures

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	composectl "github.com/foundriesio/composeapp/cmd/composectl/cmd"
	rand2 "math/rand"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"
)

var (
	composeExec = os.Getenv("COMPOSECTL_EXE")
)

type (
	App struct {
		Name         string
		BaseUri      string
		PublishedUri string
		Dir          string
	}
)

func NewApp(t *testing.T, composeDef string, appName ...string) *App {
	var name string
	if len(appName) > 0 {
		name = appName[0]
	} else {
		name = randomString(5)
	}
	appDir := path.Join(t.TempDir(), name)
	err := os.MkdirAll(appDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(appDir, "docker-compose.yml"), []byte(composeDef), 0o640)
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		Name:    name,
		BaseUri: "registry:5000/factory/" + name,
		Dir:     appDir,
	}
}

func (a *App) Publish(t *testing.T) {
	t.Run("publish app", func(t *testing.T) {
		digestFile := path.Join(a.Dir, "digest.sha256")
		tag, err := randomStringCrypto(7)
		if err != nil {
			t.Fatalf("failed to generate a random image tag value: %s\n", err)
		}
		runCmd(t, a.Dir, "publish", "-d", digestFile, a.BaseUri+":"+tag, "amd64")
		if b, err := os.ReadFile(digestFile); err == nil {
			a.PublishedUri = a.BaseUri + "@" + string(b)
		} else {
			t.Fatalf("failed to read the published app digest: %s\n", err)
		}
		fmt.Printf("published app uri: %s\n", a.PublishedUri)
	})
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
	a.runCmd(t, "uninstall app", "uninstall", a.Name)
}

func (a *App) Run(t *testing.T) {
	a.runCmd(t, "run app", "run", a.Name)
}

func (a *App) Up(t *testing.T) {
	t.Run("compose up", func(t *testing.T) {
		homeDir, homeDirErr := os.UserHomeDir()
		if homeDirErr != nil {
			t.Errorf("failed to get home directory path: %s\n", homeDirErr)
		}
		composeRoot := path.Join(homeDir, ".composeapps/projects", a.Name)

		c := exec.Command("docker", "compose", "up", "--remove-orphans", "-d")
		c.Dir = composeRoot
		output, err := c.CombinedOutput()
		if err != nil {
			t.Errorf("failed to run `docker compose up -d` command: %s\n", output)
		}
	})
}

func (a *App) Stop(t *testing.T) {
	a.runCmd(t, "stop app", "stop", a.Name)
}

func (a *App) CheckFetched(t *testing.T) {
	t.Run("list app", func(t *testing.T) {
		output := runCmd(t, a.Dir, "ls", "--format", "json")
		var lsOutput []composectl.AppJsonOutput
		if err := json.Unmarshal(output, &lsOutput); err != nil {
			t.Errorf("failed to unmarshal app list output: %s\n", err)
		}
		if a.PublishedUri != lsOutput[0].URI {
			t.Errorf("app uri in the list output does not equal to the published app;"+
				" published app uri: %s, app list uri: %s\n", a.PublishedUri, lsOutput[0].URI)
		}
	})
	t.Run("check app", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		if err := json.Unmarshal(output, &checkResult); err != nil {
			t.Errorf("failed to unmarshal check app result: %s\n", err)
		}
		if len(checkResult.FetchCheck.MissingBlobs) > 0 {
			t.Errorf("There are missing app blobs: %+v\n", checkResult.FetchCheck.MissingBlobs)
		}
	})
}

func (a *App) CheckInstalled(t *testing.T) {
	t.Run("check if installed", func(t *testing.T) {
		output := runCmd(t, a.Dir, "check", "--local", "--install", a.PublishedUri, "--format", "json")
		checkResult := composectl.CheckAndInstallResult{}
		if err := json.Unmarshal(output, &checkResult); err != nil {
			t.Errorf("failed to unmarshal check app result: %s\n", err)
		}

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
		if err := json.Unmarshal(output, &psOutput); err != nil {
			t.Errorf("failed to unmarshal app ps output: %s\n", err)
		}
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
	if err != nil {
		t.Fatalf("failed to run `%s` command: %s\n", args[0], output)
	}
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
