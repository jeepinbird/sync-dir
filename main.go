package main

import (
	"github.com/jeepinbird/sync-dir/cmd"
	"os"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
