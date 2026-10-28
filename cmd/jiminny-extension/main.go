// Command jiminny-extension serves the Jiminny extension over Open Conduit's
// extension protocol on stdin/stdout. Its package-manifest and
// package-certification subcommands write the package files, and
// certify-fixture serves a fake Jiminny for `open-conduit extension certify`.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/open-conduit/open-conduit-jiminny/jiminny"
	"github.com/open-conduit/open-conduit-jiminny/jiminny/jiminnytest"
	"github.com/open-conduit/open-conduit/protocol/sdk"
)

func main() {
	if sdk.PackageCommand(os.Args[1:], jiminny.Manifest(), jiminny.Certification(), os.Stdout) {
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "certify-fixture" {
		if err := serveFixture(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	logger := log.New(os.Stderr, "jiminny: ", log.LstdFlags|log.LUTC)
	if err := sdk.Serve(jiminny.Build(jiminny.Options{Log: logger})); err != nil {
		logger.Print(err)
		os.Exit(1)
	}
}

// serveFixture runs a fake Jiminny with two synthetic calls, prints the
// fixture over it and serves until stdin closes.
func serveFixture(stdin io.Reader, stdout io.Writer) error {
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	call := jiminnytest.Call("8d0f3a9e-0000-4000-8000-000000000001", "Weekly progress", at)
	fake := jiminnytest.New(call, jiminnytest.Call("8d0f3a9e-0000-4000-8000-000000000002", "Discovery workshop", at.Add(-time.Hour)))
	defer fake.Close()
	f, err := jiminny.NewFixture(fake.URL, jiminnytest.Token, call, at)
	if err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err := stdout.Write(append(b, '\n')); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, stdin)
	return nil
}
