// devmg is the guest-side devm dispatcher: the only devm-owned binary
// installed inside the VM. `devm` never exists on the guest and `devmg`
// never exists on the Mac — an agent's PATH tells it which side it is
// on without ambiguity.
//
// Subcommands:
//
//	devmg pop <path-or-url> [-- <open-args>...]
//	devmg propose [--reason <text>] [--kind devm.yaml|devm.me.yaml|devm.sh|devm.me.sh]
//	devmg run <command>
//
// Each subcommand reaches the Mac-side daemon over softnet (pop, propose)
// or reads the local guest command manifest and re-execs bash (run).
// The per-subcommand main body is factored into <sub>Main funcs so
// tests exercise them without spawning a subprocess.
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
	sub := os.Args[1]
	args := os.Args[2:]
	switch sub {
	case "pop":
		os.Exit(popMain(args))
	case "propose":
		os.Exit(proposeMain(args))
	case "run":
		os.Exit(runMain(args))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "devmg: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `devmg — guest-side devm dispatcher

Usage: devmg <subcommand> [args...]

Subcommands:
  pop      Open a file with its default Mac app.
  propose  Signal that a devm.yaml (or devm.me.yaml, devm.sh, devm.me.sh)
           edit is ready for the Mac-side reviewer.
  run      Invoke a named function from this project's command manifest.

Every subcommand carries its own -h/--help.`)
}
