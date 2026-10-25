package main

import (
	"git.host.bzh/pepe/kuberpack/internal/cli"
	"git.host.bzh/pepe/kuberpack/internal/platformcli"
	"os"
)

func main() {
	os.Exit(cli.Main(cli.Options{Admin: true, Install: platformcli.Install, Doctor: platformcli.Doctor}))
}
