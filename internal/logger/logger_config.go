package logger

import (
	"os"
	"strings"

	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/env"
)

const (
	DefaultLoggerLevel   = "info"
	DefaultPrettyLogs    = true
	DefaultFileOutput    = true
	DefaultLogDir        = "./logs"
	DefaultLogSizeMB     = 1
	DefaultLogMaxBackups = 7
	DefaultLogMaxAgeDays = 14
	DefaultTheme         = "default"
)

// BuildConfigFromConfig creates a logger.Config from both env and YAML config values.
//
// Environment variables have priority, including the existing env-only logger vars.
func BuildConfigFromConfig(cfg *config.Config) *Config {
	loggerCfg := &Config{
		Level:      env.GetEnvOrDefault("OLLA_LOG_LEVEL", DefaultLoggerLevel),
		PrettyLogs: env.GetEnvBoolOrDefault("OLLA_PRETTY_LOGS", DefaultPrettyLogs),
		FileOutput: env.GetEnvBoolOrDefault("OLLA_FILE_OUTPUT", DefaultFileOutput),
		LogDir:     env.GetEnvOrDefault("OLLA_LOG_DIR", DefaultLogDir),
		MaxSize:    env.GetEnvIntOrDefault("OLLA_LOG_SIZE_MB", DefaultLogSizeMB),
		MaxBackups: env.GetEnvIntOrDefault("OLLA_LOG_MAX_BACKUPS", DefaultLogMaxBackups),
		MaxAge:     env.GetEnvIntOrDefault("OLLA_LOG_MAX_AGE_DAYS", DefaultLogMaxAgeDays),
		Theme:      env.GetEnvOrDefault("OLLA_THEME", DefaultTheme),
	}

	if cfg == nil || cfg.Filename == "" {
		return loggerCfg
	}

	if os.Getenv("OLLA_LOG_LEVEL") == "" {
		applyLoggingLevel(loggerCfg, cfg.Logging.Level)
	}

	if os.Getenv("OLLA_PRETTY_LOGS") == "" {
		applyLoggingFormat(loggerCfg, cfg.Logging.Format)
	}

	if os.Getenv("OLLA_FILE_OUTPUT") == "" {
		applyLoggingOutput(loggerCfg, cfg.Logging.Output)
	}

	return loggerCfg
}

func applyLoggingLevel(cfg *Config, level string) {
	if level != "" {
		cfg.Level = level
	}
}

func applyLoggingFormat(cfg *Config, format string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		cfg.PrettyLogs = false
	case "text":
		cfg.PrettyLogs = true
	}
}

func applyLoggingOutput(cfg *Config, output string) {
	switch strings.ToLower(strings.TrimSpace(output)) {
	case "stdout":
		cfg.FileOutput = false
	case "file":
		cfg.FileOutput = true
	}
}
