// Command hal9001 is the hal9001 daemon and its command line.
package main

import (
	"context"
	"os"

	"hal9001/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], cli.Env{Version: version}))
}
