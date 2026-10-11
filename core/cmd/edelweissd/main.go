// Command edelweissd is the edelweiss web admin daemon and CLI.
package main

import (
	"os"

	"github.com/Pukka922/edelweiss/core/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
