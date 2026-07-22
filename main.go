// Command foreman-runner is the agent runner for foreman.
//
// It enrolls a machine with a foreman server and (in later milestones) polls
// for tasks, verifies their signatures, and runs the coding agent inside a
// tmux session with log capture.
//
// Subcommands:
//
//	enroll   register this machine with a foreman server
//	run      heartbeat daemon: report agents and host metrics to the server
//	spawn    launch a coding agent in a tmux session (proof of concept)
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "enroll":
		err = cmdEnroll(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "spawn":
		err = cmdSpawn(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `foreman-runner - agent runner for foreman

usage:
  foreman-runner <command> [flags]

commands:
  enroll   register this machine with a foreman server
  run      heartbeat daemon: report agents and host metrics to the server
  spawn    launch a coding agent in a tmux session (proof of concept)

run "foreman-runner <command> -h" for command flags
`)
}
