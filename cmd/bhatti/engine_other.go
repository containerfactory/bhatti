//go:build !linux

package main

import (
	"fmt"

	"github.com/sahilshubham/bhatti/pkg"
	"github.com/sahilshubham/bhatti/pkg/engine"
)

func newFirecrackerEngine(cfg *pkg.Config) (engine.Engine, error) {
	return nil, fmt.Errorf("firecracker engine is only available on Linux")
}
