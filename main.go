package main

import (
	"fmt"
	"os"

	"imo/internal/webapp"
)

func main() {
	if err := webapp.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
