package main

import (
	"os"

	"github.com/Harry10799/kubetrace/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
