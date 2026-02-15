package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLogger creates a zap logger for the given environment.
// prod uses JSON output, local/dev use colored console output.
// levelOverride (if non-empty) overrides the log level: debug, info, warn, error.
func NewLogger(env string, levelOverride ...string) (*zap.Logger, error) {
	var cfg zap.Config
	switch env {
	case "prod":
		cfg = zap.NewProductionConfig()
	case "local", "dev", "docker":
		cfg = zap.NewDevelopmentConfig()
	default:
		return nil, fmt.Errorf("unknown environment %q for logger", env)
	}

	if len(levelOverride) > 0 && levelOverride[0] != "" {
		var level zapcore.Level
		if err := level.UnmarshalText([]byte(levelOverride[0])); err != nil {
			return nil, fmt.Errorf("invalid log level %q: %w", levelOverride[0], err)
		}
		cfg.Level = zap.NewAtomicLevelAt(level)
	}

	l, err := cfg.Build(zap.AddStacktrace(zapcore.ErrorLevel))
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	return l, nil
}
