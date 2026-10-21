// Command ptit-eript-api starts the HTTP API: `go run .` (reads .env from the working directory).
// Same server as ./cmd/server; the worker and CLI stay in ./cmd/worker and ./cmd/cli.
package main

import (
	"fmt"
	"os"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/serverapp"
)

func main() {
	if err := serverapp.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}
