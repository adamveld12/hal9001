//go:build !darwin

// Command hal9001-app is the HAL9001 macOS app; it only runs on macOS.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "hal9001-app runs only on macOS")
	os.Exit(1)
}
