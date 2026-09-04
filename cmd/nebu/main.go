// Command nebu is the daemon and client in one binary.
package main

import (
	"os"

	"github.com/nickheyer/nebu/internal/cli"
	"github.com/nickheyer/nebu/pkg/proc"
)

func main() {
	proc.Init()
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
