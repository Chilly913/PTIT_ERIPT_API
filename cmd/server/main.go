// Command server runs the HTTP API.
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
