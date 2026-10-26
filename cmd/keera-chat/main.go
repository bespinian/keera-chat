// Command keera-chat is a browser chat that signs in and sends messages
// through Keera Gateway.
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
