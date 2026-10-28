// Command fake-jiminny serves two synthetic processed calls on a local
// fake of Jiminny's API, for trying the extension without credentials. It
// writes its URL to the file named by its argument.

package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/open-conduit/open-conduit-jiminny/jiminny/jiminnytest"
)

func main() {
	now := time.Now().UTC().Truncate(time.Second)
	s := jiminnytest.New(
		jiminnytest.Call("11111111-0000-4000-8000-000000000001", "Weekly progress", now.Add(-3*time.Hour)),
		jiminnytest.Call("11111111-0000-4000-8000-000000000002", "Discovery workshop", now.Add(-50*time.Hour)))
	os.WriteFile(os.Args[1], []byte(s.URL), 0o600)
	fmt.Println(s.URL)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}
