package main

import (
	"os"

	"github.com/polera/tokenhawk/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], version))
}
