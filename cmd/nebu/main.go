// Command nebu is the daemon and client in one binary.
package main

import (
	"os"

	"github.com/nickheyer/nebu/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
