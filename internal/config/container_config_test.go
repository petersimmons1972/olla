package config

import (
	"fmt"
	"io/fs"
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

func TestReleaseDockerContextCanGenerateDockerConfig(t *testing.T) {
	releaseSource := t.TempDir()
	prepareReleaseDockerSource(t, releaseSource)

	for i, extraFiles := range goreleaserDockerExtraFileSets(t) {
		t.Run(fmt.Sprintf("docker_%d", i+1), func(t *testing.T) {
			ctx := t.TempDir()

			for _, entry := range extraFiles {
				mustCopyReleaseEntry(t, releaseSource, ctx, entry)
			}

			cmd := exec.Command("sh", "./scripts/generate-container-config.sh")
			cmd.Dir = ctx
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("release Docker context cannot generate config/docker.yaml: %v\n%s", err, output)
			}

			if _, err := os.Stat(filepath.Join(ctx, "config", "docker.yaml")); err != nil {
				t.Fatalf("release Docker context did not produce config/docker.yaml: %v", err)
			}
		})
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

func prepareReleaseDockerSource(t *testing.T, dir string) {
	t.Helper()

	mustCopyFile(t, filepath.Join(repoRoot, "config/config.yaml"), filepath.Join(dir, "config", "config.yaml"))
	mustCopyFile(t, filepath.Join(repoRoot, "config/models.yaml"), filepath.Join(dir, "config", "models.yaml"))
	mustCopyFile(t, filepath.Join(repoRoot, "scripts/generate-container-config.sh"), filepath.Join(dir, "scripts", "generate-container-config.sh"))
	mustCopyDir(t, filepath.Join(repoRoot, "config/profiles"), filepath.Join(dir, "config", "profiles"))

	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatalf("create release logs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", ".gitkeep"), nil, 0o644); err != nil {
		t.Fatalf("write release logs .gitkeep: %v", err)
	}

	cmd := exec.Command("bash", "./scripts/generate-container-config.sh")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare release docker source: %v\n%s", err, output)
	}

	mustCopyFile(t, filepath.Join(dir, "config", "docker.yaml"), filepath.Join(dir, "config.yaml"))
}

func mustCopyReleaseEntry(t *testing.T, srcRoot, dstRoot, entry string) {
	t.Helper()

	cleanEntry := filepath.Clean(entry)
	if strings.HasSuffix(entry, "/") {
		mustCopyDir(t, filepath.Join(srcRoot, cleanEntry), filepath.Join(dstRoot, cleanEntry))
		return
	}

	mustCopyFile(t, filepath.Join(srcRoot, cleanEntry), filepath.Join(dstRoot, cleanEntry))
}

func mustCopyDir(t *testing.T, src, dst string) {
	t.Helper()

	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		mustCopyFile(t, path, target)
		return nil
	}); err != nil {
		t.Fatalf("copy dir %s to %s: %v", src, dst, err)
	}
}

func goreleaserDockerExtraFileSets(t *testing.T) [][]string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}

	var (
		sets        [][]string
		inDockers   bool
		extraIndent = -1
	)

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inDockers = trimmed == "dockers:"
			extraIndent = -1
			continue
		}
		if !inDockers {
			continue
		}

		if strings.HasPrefix(trimmed, "extra_files:") {
			sets = append(sets, nil)
			extraIndent = indent
			continue
		}
		if extraIndent == -1 {
			continue
		}
		if indent <= extraIndent {
			extraIndent = -1
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}

		entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if commentIndex := strings.Index(entry, " #"); commentIndex >= 0 {
			entry = strings.TrimSpace(entry[:commentIndex])
		}
		entry = strings.Trim(entry, `"'`)
		if entry != "" {
			sets[len(sets)-1] = append(sets[len(sets)-1], entry)
		}
	}

	if len(sets) == 0 {
		t.Fatalf(".goreleaser.yml has no Docker extra_files entries")
	}

	return sets
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
