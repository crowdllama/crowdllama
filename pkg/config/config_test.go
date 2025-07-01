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
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

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

func TestLoadFromEnvironment(t *testing.T) {
	// Test case 1: No environment variables set
	cfg := NewConfiguration()
	cfg.LoadFromEnvironment()

	if cfg.Verbose != false {
		t.Errorf("Expected Verbose to be false, got %v", cfg.Verbose)
	}

	if cfg.KeyPath != "" {
		t.Errorf("Expected KeyPath to be empty, got %s", cfg.KeyPath)
	}

	if cfg.GetOllamaURL() != "http://localhost:11434/api/chat" {
		t.Errorf("Expected OllamaURL to be default, got %s", cfg.GetOllamaURL())
	}

	// Test case 2: Environment variables set
	os.Setenv("CROWDLLAMA_VERBOSE", "true")
	os.Setenv("CROWDLLAMA_KEY_PATH", "/custom/key/path")
	os.Setenv("CROWDLLAMA_OLLAMA_URL", "http://custom-ollama:11434/api/chat")
	defer func() {
		os.Unsetenv("CROWDLLAMA_VERBOSE")
		os.Unsetenv("CROWDLLAMA_KEY_PATH")
		os.Unsetenv("CROWDLLAMA_OLLAMA_URL")
	}()

	cfg2 := NewConfiguration()
	cfg2.LoadFromEnvironment()

	if cfg2.Verbose != true {
		t.Errorf("Expected Verbose to be true, got %v", cfg2.Verbose)
	}

	if cfg2.KeyPath != "/custom/key/path" {
		t.Errorf("Expected KeyPath to be /custom/key/path, got %s", cfg2.KeyPath)
	}

	if cfg2.GetOllamaURL() != "http://custom-ollama:11434/api/chat" {
		t.Errorf("Expected OllamaURL to be custom, got %s", cfg2.GetOllamaURL())
	}
}
