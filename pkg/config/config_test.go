package config

import (
	"flag"
	"os"
	"testing"
)

func TestNewConfiguration(t *testing.T) {
	config := NewConfiguration()

	if config == nil {
		t.Fatal("Expected configuration to be created, got nil")
	}

	if config.Verbose != false {
		t.Errorf("Expected Verbose to be false by default, got %v", config.Verbose)
	}

	if config.Logger != nil {
		t.Error("Expected Logger to be nil by default")
	}
}

func TestParseFlags(t *testing.T) {
	// Save original command line arguments
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	// Set test arguments
	os.Args = []string{"test", "-verbose"}

	config := NewConfiguration()
	flagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	config.ParseFlags(flagSet)

	// Parse the flags
	flagSet.Parse(os.Args[1:])

	if !config.Verbose {
		t.Error("Expected Verbose to be true after parsing -verbose flag")
	}
}

func TestIsVerbose(t *testing.T) {
	config := NewConfiguration()

	// Test default value
	if config.IsVerbose() {
		t.Error("Expected IsVerbose to return false by default")
	}

	// Test when verbose is set
	config.Verbose = true
	if !config.IsVerbose() {
		t.Error("Expected IsVerbose to return true when Verbose is set")
	}
}

func TestSetupLogger(t *testing.T) {
	config := NewConfiguration()
	config.Verbose = true

	err := config.SetupLogger()
	if err != nil {
		t.Fatalf("SetupLogger failed: %v", err)
	}

	// Verify logger was created
	logger := config.GetLogger()
	if logger == nil {
		t.Error("Expected logger to be created, got nil")
	}
}

func TestSetupLoggerProduction(t *testing.T) {
	config := NewConfiguration()
	config.Verbose = false

	err := config.SetupLogger()
	if err != nil {
		t.Fatalf("SetupLogger failed: %v", err)
	}

	// Verify logger was created
	logger := config.GetLogger()
	if logger == nil {
		t.Error("Expected logger to be created, got nil")
	}
}

func TestGetLogger(t *testing.T) {
	config := NewConfiguration()

	// Test that GetLogger returns a logger even if SetupLogger wasn't called
	logger := config.GetLogger()
	if logger == nil {
		t.Error("Expected GetLogger to return a logger, got nil")
	}
}
