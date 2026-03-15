//go:build linux

package main

import (
	"github.com/sahilshubham/bhatti/pkg"
	"github.com/sahilshubham/bhatti/pkg/engine"
	fc "github.com/sahilshubham/bhatti/pkg/engine/firecracker"
)

func newFirecrackerEngine(cfg *pkg.Config) (engine.Engine, error) {
	return fc.New(fc.Config{
		DataDir:    cfg.DataDir,
		KernelPath: cfg.FirecrackerKernel,
		BaseRootfs: cfg.FirecrackerRootfs,
		FCBinary:   cfg.FirecrackerBin,
	})
}
