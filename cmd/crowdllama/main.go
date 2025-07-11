// Package main provides the main CLI command for CrowdLlama.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/matiasinsaurralde/crowdllama/pkg/config"
	"github.com/matiasinsaurralde/crowdllama/pkg/version"

	"github.com/ollama/ollama/cmd"
)

var (
	cfg    *config.Configuration
	logger *zap.Logger
)

func main() {
	// Setup logging first
	if err := setupLogging(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup logging: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if syncErr := logger.Sync(); syncErr != nil {
			fmt.Fprintf(os.Stderr, "failed to sync logger: %v\n", syncErr)
		}
	}()

	// Initialize and customize the ollama CLI:
	ollamaCmd := cmd.NewCLI()
	ollamaCmd.Root().Use = "crowdllama"
	ollamaCmd.Root().Short = "CrowdLlama CLI - A distributed AI inference platform"
	ollamaCmd.Root().Long = `CrowdLlama CLI provides a command-line interface for the CrowdLlama distributed AI inference platform.`
	ollamaCmd.Root().SilenceUsage = true
	ollamaCmd.Root().SilenceErrors = true

	// Add our custom commands
	ollamaCmd.AddCommand(networkStatusCmd)
	ollamaCmd.AddCommand(versionCmd)

	// Start ollama server in background
	go func() {
		logger.Info("Starting Ollama server in background")
		ollamaCmd.Root().Run(ollamaCmd.Root(), []string{})
	}()

	// Execute the ollama CLI with our modifications:
	if err := ollamaCmd.Execute(); err != nil {
		logger.Error("CLI execution failed", zap.Error(err))
		os.Exit(1)
	}
}

var networkStatusCmd = &cobra.Command{
	Use:   "network-status",
	Short: "Get the status of the network",
	Long:  `Get the status of the CrowdLlama network and connected peers.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := runNetworkStatus(); err != nil {
			logger.Error("Failed to get network status", zap.Error(err))
			os.Exit(1)
		}
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Long:  `Print detailed version information including commit hash, build date, and Go version.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := runVersion(); err != nil {
			logger.Error("Failed to get version", zap.Error(err))
			os.Exit(1)
		}
	},
}

func setupLogging() error {
	// Initialize configuration
	cfg = config.NewConfiguration()

	// Parse command line flags
	flagSet := flag.NewFlagSet("crowdllama", flag.ExitOnError)
	cfg.ParseFlags(flagSet)

	// Load from environment variables
	cfg.LoadFromEnvironment()

	// Setup logger
	if err := cfg.SetupLogger(); err != nil {
		return fmt.Errorf("failed to setup logger: %w", err)
	}

	logger = cfg.GetLogger()

	// Log startup information
	logger.Info("CrowdLlama CLI version", zap.String("version", version.String()))

	if cfg.IsVerbose() {
		logger.Info("Verbose mode enabled")
	}

	return nil
}

func runVersion() error {
	logger.Info("Displaying version information")
	fmt.Println(version.String())
	return nil
}

func runNetworkStatus() error {
	logger.Info("Checking network status")

	// TODO: Implement actual network status checking
	// For now, just display a placeholder message
	logger.Info("Network status check completed", zap.String("status", "placeholder"))
	fmt.Println("Network status: Placeholder - implementation pending")

	return nil
}
