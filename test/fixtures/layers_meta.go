package fixtures

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func GenerateLayersMetaFile(t *testing.T, appsRootDir string) string {
	tmpDir := t.TempDir()
	layerSizesFile := filepath.Join(tmpDir, "layers_meta.json")
	c := exec.Command("python", os.Getenv("LAYERS_SIZE_SCRIPT"), "--apps-root",
		appsRootDir, "-o", layerSizesFile)
	output, err := c.CombinedOutput()
	Checkf(t, err, "failed to run command to gather app layer sizes: %s", string(output))
	b, err := os.ReadFile(layerSizesFile)
	Check(t, err)
	var ls map[string]interface{}
	Check(t, json.Unmarshal(b, &ls))
	lsPerArch := map[string]interface{}{"amd64": ls}
	b, err = json.Marshal(lsPerArch)
	Check(t, err)
	layersMetaFile := filepath.Join(tmpDir, "layers_meta_per_arch.json")
	Check(t, os.WriteFile(layersMetaFile, b, 0x644))
	return layersMetaFile
}
