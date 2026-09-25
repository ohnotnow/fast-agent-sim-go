package main

import (
	"os"

	"github.com/ohnotnow/fast-agent-sim-go/internal/fas"
)

func main() {
	os.Exit(fas.Run(os.Args[1:]))
}
