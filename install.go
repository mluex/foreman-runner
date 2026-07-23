package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mluex/foreman-runner/internal/service"
)

// cmdInstall installs the runner as a background service that starts on boot.
// It copies the binary to a stable location and writes the platform service
// file, then prints the command to enable it (it does not enable it itself).
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	home := service.DefaultHome()
	if home == "" {
		return fmt.Errorf("could not determine the home directory")
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	plan, err := service.BuildPlan(service.CurrentGOOS(), home, os.Getenv("PATH"))
	if err != nil {
		return err
	}
	if err := plan.Apply(self); err != nil {
		return err
	}

	fmt.Printf("binary    %s\n", plan.BinaryDest)
	fmt.Printf("service   %s\n", plan.UnitPath)
	fmt.Print("\nto start it now and on every boot, run:\n")
	for _, cmd := range plan.EnableCommands {
		fmt.Printf("  %s\n", cmd)
	}
	fmt.Printf("\nadd %s to your PATH to call \"foreman-runner\" without \"./\".\n", filepath.Dir(plan.BinaryDest))

	return nil
}
