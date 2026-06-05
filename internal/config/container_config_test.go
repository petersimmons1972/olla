package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

func TestGeneratedDockerConfigBindsAllInterfaces(t *testing.T) {
	tmp := t.TempDir()

	mustCopyFile(t, filepath.Join(repoRoot, "config/config.yaml"), filepath.Join(tmp, "config", "config.yaml"))
	mustCopyFile(t, filepath.Join(repoRoot, "scripts/generate-container-config.sh"), filepath.Join(tmp, "scripts", "generate-container-config.sh"))

	cmd := exec.Command("bash", "./scripts/generate-container-config.sh")
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generate-container-config.sh failed: %v\n%s", err, output)
	}

	generated, err := os.ReadFile(filepath.Join(tmp, "config", "docker.yaml"))
	if err != nil {
		t.Fatalf("read generated docker.yaml: %v", err)
	}

	if !strings.Contains(string(generated), `host: "0.0.0.0"`) {
		t.Fatalf("generated docker config must bind to 0.0.0.0, got:\n%s", generated)
	}
}

func TestDockerfileUsesGeneratedDockerConfigByDefault(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	if !strings.Contains(string(data), "ENV OLLA_CONFIG_FILE=/app/config/docker.yaml") {
		t.Fatalf("Dockerfile must default OLLA_CONFIG_FILE to /app/config/docker.yaml so published ports are reachable")
	}
}

func TestLocalDockerBuildGeneratesDockerConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "makefile"))
	if err != nil {
		t.Fatalf("read makefile: %v", err)
	}

	target := extractMakeTarget(string(data), "docker-build-local")
	if target == "" {
		t.Fatalf("docker-build-local target not found")
	}

	generateIndex := strings.Index(target, "bash ./scripts/generate-container-config.sh")
	buildIndex := strings.Index(target, "docker build")
	if generateIndex == -1 {
		t.Fatalf("docker-build-local must generate config/docker.yaml before building the image")
	}
	if buildIndex == -1 {
		t.Fatalf("docker-build-local target does not run docker build")
	}
	if generateIndex > buildIndex {
		t.Fatalf("docker-build-local must generate config/docker.yaml before docker build")
	}
}

func mustCopyFile(t *testing.T, src, dst string) {
	t.Helper()

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", dst, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func extractMakeTarget(makefile, target string) string {
	lines := strings.Split(makefile, "\n")
	header := target + ":"

	for i, line := range lines {
		if strings.TrimSpace(line) != header {
			continue
		}

		var body []string
		for _, candidate := range lines[i+1:] {
			if strings.HasPrefix(candidate, "\t") || strings.TrimSpace(candidate) == "" {
				body = append(body, candidate)
				continue
			}
			break
		}

		return strings.Join(body, "\n")
	}

	return ""
}
