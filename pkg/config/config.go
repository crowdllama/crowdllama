package config

import (
	"flag"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// WorkerCfg contains worker-specific configuration
type WorkerCfg struct {
	// Worker-specific fields can be added here in the future
}

// ConsumerCfg contains consumer-specific configuration
type ConsumerCfg struct {
	// Consumer-specific fields can be added here in the future
}

// Configuration is the main configuration structure that embeds worker and consumer configs
type Configuration struct {
	Verbose bool
	KeyPath string // Path to the private key file
	Logger  *zap.Logger
	WorkerCfg
	ConsumerCfg
}

// NewConfiguration creates a new configuration with default values
func NewConfiguration() *Configuration {
	return &Configuration{
		Verbose: false,
		KeyPath: "", // Will be set to default if not provided
	}
}

// ParseFlags parses command line flags and updates the configuration
func (cfg *Configuration) ParseFlags(flagSet *flag.FlagSet) {
	flagSet.BoolVar(&cfg.Verbose, "verbose", false, "Enable verbose logging")
	flagSet.StringVar(&cfg.KeyPath, "key", "", "Path to private key file (default: ~/.crowdllama/<component>.key)")
}

// IsVerbose returns true if verbose logging is enabled
func (cfg *Configuration) IsVerbose() bool {
	return cfg.Verbose
}

// SetupLogger initializes the zap logger based on configuration
func (cfg *Configuration) SetupLogger() error {
	var logger *zap.Logger
	var err error

	if cfg.Verbose {
		// Development logger with debug level and console encoding
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		logger, err = config.Build()
	} else {
		// Production logger with info level and JSON encoding
		config := zap.NewProductionConfig()
		logger, err = config.Build()
	}

	if err != nil {
		return err
	}

	cfg.Logger = logger
	return nil
}

// GetLogger returns the configured logger
func (cfg *Configuration) GetLogger() *zap.Logger {
	if cfg.Logger == nil {
		// Fallback to a basic logger if not configured
		logger, _ := zap.NewProduction()
		return logger
	}
	return cfg.Logger
}
