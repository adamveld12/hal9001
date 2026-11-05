// Command fake-acp-agent is a scripted ACP agent for tests. See README.md.
package main

import (
	"os"

	"hal9001/internal/agent/fakeagent"
)

func main() {
	os.Exit(fakeagent.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
