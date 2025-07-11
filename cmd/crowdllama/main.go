// Package main provides the main CLI command for CrowdLlama.
package main

import (
	"fmt"
	"os"

	"github.com/matiasinsaurralde/crowdllama/pkg/version"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: crowdllama <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  version   Print the version information")
		return
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(version.String())
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		return
	}
}
