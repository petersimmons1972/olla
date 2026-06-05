package config

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeHealthcheckUsesInstalledImageTool(t *testing.T) {
	composeData, err := os.ReadFile("../../docker-compose.yaml")
	if err != nil {
		t.Fatalf("read docker-compose.yaml: %v", err)
	}

	var compose struct {
		Services map[string]struct {
			Healthcheck struct {
				Test []string `yaml:"test"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeData, &compose); err != nil {
		t.Fatalf("parse docker-compose.yaml: %v", err)
	}

	olla, ok := compose.Services["olla"]
	if !ok {
		t.Fatalf("compose service olla not found")
	}
	if len(olla.Healthcheck.Test) < 2 {
		t.Fatalf("compose healthcheck must use exec form with a command, got %#v", olla.Healthcheck.Test)
	}

	healthcheckTool := olla.Healthcheck.Test[1]

	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	installedTools := alpinePackages(string(dockerfile))

	if !installedTools[healthcheckTool] {
		t.Fatalf("compose healthcheck uses %q, but Dockerfile installs %v", healthcheckTool, mapKeys(installedTools))
	}
}

func alpinePackages(dockerfile string) map[string]bool {
	re := regexp.MustCompile(`apk\s+--no-cache\s+add\s+([^&\n]+)`)
	match := re.FindStringSubmatch(dockerfile)
	if len(match) < 2 {
		return map[string]bool{}
	}

	packages := make(map[string]bool)
	for _, field := range strings.Fields(match[1]) {
		packages[strings.TrimSpace(field)] = true
	}
	return packages
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
