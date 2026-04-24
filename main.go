package main

import "github.com/customerio/cli/cmd"

// version is set at build time via -ldflags "-X main.version=..."
var version string

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
