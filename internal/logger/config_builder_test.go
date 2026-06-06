package logger

import (
	"os"
	"testing"

	"github.com/thushan/olla/internal/config"
)

func TestBuildConfigFromConfig_PrioritizesEnvBeforeYAML(t *testing.T) {
	t.Setenv("OLLA_LOG_LEVEL", "warn")
	t.Setenv("OLLA_PRETTY_LOGS", "false")
	t.Setenv("OLLA_FILE_OUTPUT", "false")

	cfg := &config.Config{
		Filename: "test-logging-config.yaml",
		Logging: config.LoggingConfig{
			Level:  "debug",
			Format: "text",
			Output: "file",
		},
	}

	loggerCfg := BuildConfigFromConfig(cfg)

	if loggerCfg.Level != "warn" {
		t.Fatalf("expected level from OLLA_LOG_LEVEL to win, got %q", loggerCfg.Level)
	}
	if loggerCfg.PrettyLogs {
		t.Fatalf("expected pretty logs to remain env-controlled, got true")
	}
	if loggerCfg.FileOutput {
		t.Fatalf("expected file output to remain env-controlled, got true")
	}
}

func TestBuildConfigFromConfig_AppliesYAMLWhenEnvNotSet(t *testing.T) {
	// Ensure logger env vars are empty so YAML config can take effect.
	t.Setenv("OLLA_LOG_LEVEL", "")
	t.Setenv("OLLA_PRETTY_LOGS", "")
	t.Setenv("OLLA_FILE_OUTPUT", "")

	tmpFile, err := os.CreateTemp("", "olla-logging-config-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	_, err = tmpFile.WriteString(`
logging:
  level: "debug"
  format: "text"
  output: "file"
`)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("failed to close temp config: %v", err)
	}

	cfg, err := config.Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to load temp config: %v", err)
	}

	loggerCfg := BuildConfigFromConfig(cfg)

	if loggerCfg.Level != "debug" {
		t.Fatalf("expected level from YAML config to apply, got %q", loggerCfg.Level)
	}
	if !loggerCfg.PrettyLogs {
		t.Fatalf("expected text output to enable pretty logs")
	}
	if !loggerCfg.FileOutput {
		t.Fatalf("expected file output to be enabled for yaml output=file")
	}
}
