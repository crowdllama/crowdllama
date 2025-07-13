// Package logutil provides shared logging utilities for CrowdLlama.
package logutil

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewAppLogger creates a new logger with consistent formatting and colored output
func NewAppLogger(appName string, verbose bool) *zap.Logger {
	cfg := zap.NewDevelopmentConfig()

	// Configure colored output
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	cfg.EncoderConfig.TimeKey = "time"
	cfg.EncoderConfig.MessageKey = "msg"
	cfg.EncoderConfig.CallerKey = ""
	cfg.EncoderConfig.StacktraceKey = ""

	// Set log level based on verbose flag
	if !verbose {
		cfg.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}

	logger, err := cfg.Build()
	if err != nil {
		// Fallback to a basic logger if config fails
		logger = zap.NewNop()
	}

	// Add app name as a field to all log entries
	return logger.With(zap.String("app", appName))
}
