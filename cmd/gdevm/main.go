// gdevm is the guest-side devm dispatcher: the only devm-owned binary
// installed inside the VM. `devm` never exists on the guest and `gdevm`
// never exists on the Mac — an agent's PATH tells it which side it is
// on without ambiguity.
//
// Subcommands:
//
//	gdevm propose     [--reason <text>] [--kind devm.yaml|devm.me.yaml|devm.sh|devm.me.sh]
//	gdevm run         <command>
//	gdevm passthrough <duration> --reason <text>
//	gdevm upgrade
//	gdevm recipes     list | get <name> | asset ls <name> | asset get <name> <path>
//	gdevm serve
//
// Each subcommand reaches the Mac-side daemon over softnet (propose,
// passthrough) or reads the local guest command manifest
// and re-execs bash (run). The per-subcommand main body is factored
// into <sub>Main funcs so tests exercise them without spawning a
// subprocess. `serve` is the exception: it's a long-running guest-side
// daemon rather than a one-shot command.
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
	case "passthrough":
		os.Exit(passthroughMain(args))
	case "upgrade":
		os.Exit(upgradeMain(args))
	case "recipes":
		os.Exit(recipesMain(args))
	case "serve":
		os.Exit(serveMain(args))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "gdevm: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `gdevm — guest-side devm dispatcher

Usage: gdevm <subcommand> [args...]

Subcommands:
  pop          Open a file on the Mac. Default: filestash URL in the
               Mac's browser. --native: macOS `+"`open`"+` on the mirrored
               Mac file (or a scratch cp when the file is out of mirror).
  propose      Signal that a devm.yaml (or devm.me.yaml, devm.sh,
               devm.me.sh) edit is ready for the Mac-side reviewer.
  run          Invoke a named function from this project's command
               manifest.
  passthrough  Request a supervised egress passthrough window. The
               human on the Mac side runs `+"`devm passthrough approve`"+`
               to authorize it before it opens.
  upgrade      Pull down the daemon's current gdevm binary,
               env template, and commands manifest into this VM.
               No args.
  recipes      Query the Mac-side recipes catalog:
                 recipes list
                 recipes get <name>
                 recipes asset ls <name>
                 recipes asset get <name> <path>
  serve        Run the guest-side state daemon in the foreground.
               Long-running; exits on SIGTERM/SIGINT. No args.

Every subcommand carries its own -h/--help.`)
}
