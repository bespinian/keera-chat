// Command keera-chat is the chat for the people behind a Keera Gateway: a
// conversation in the browser, signed in and sent through the gateway.
package main

import (
	"fmt"
	"os"

	"github.com/bespinian/keera-chat/internal/server"
	"github.com/bespinian/keera-chat/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.String())
		return
	}
	if err := server.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "keera-chat:", err)
		os.Exit(1)
	}
}
