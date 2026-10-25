package main

import (
	"git.host.bzh/pepe/kuberpack/internal/cli"
	"os"
)

func main() { os.Exit(cli.Main(cli.Options{})) }
