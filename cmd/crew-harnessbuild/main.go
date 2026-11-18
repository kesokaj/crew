// Command crew-harnessbuild builds Crew's harness into a home skeleton for
// every agent CLI (design 12.1). The runtime image build runs it in a Go
// build stage and copies the result into the image's home skeleton:
//
//	crew-harnessbuild -src harness -out DIR
//
// DIR is new or empty. It exits 1 when the build refuses a file, with the
// reason on standard error, and 2 on a usage error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kesokaj/crew/internal/harnessbuild"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("crew-harnessbuild", flag.ContinueOnError)
	fl.SetOutput(stderr)
	src := fl.String("src", "", "the harness tree: context/, skills/, agents/, rules/")
	out := fl.String("out", "", "the directory to write the home skeleton into; new or empty")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *src == "" || *out == "" || fl.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: crew-harnessbuild -src DIR -out DIR")
		return 2
	}
	sk, err := harnessbuild.Plan(harnessbuild.DirSource(*src))
	if err != nil {
		fmt.Fprintln(stderr, "crew-harnessbuild:", err)
		return 1
	}
	if err := harnessbuild.Write(*out, sk.Files); err != nil {
		fmt.Fprintln(stderr, "crew-harnessbuild:", err)
		return 1
	}
	inv := sk.Inventory
	fmt.Fprintf(stdout, "crew-harnessbuild: %d skills, %d agents, %d rules; %d files for agy; context %d bytes, agy rules block %d of %d\n",
		len(inv.Skills), len(inv.Agents), len(inv.Rules), len(sk.Files), sk.ContextBytes, sk.AgyRulesBytes, harnessbuild.ContextBudget)
	return 0
}
