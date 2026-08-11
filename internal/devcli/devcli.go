// Package devcli is the `mise run dev` REPL: a fake radio with a fixed
// client, stdin as the mesh. Type a message, see what the bot would send.
package devcli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/jclement/owgbot/internal/transport/fake"
)

// DevUser is the fixed pubkey prefix the REPL sends from. It is also listed
// as an admin in the dev config so admin commands are testable.
const DevUser = "deadbeef0001"

// Run drives the REPL until stdin closes. Outbound messages are printed as
// they would hit the mesh, one line per radio chunk.
func Run(tr *fake.Transport) {
	go func() {
		for m := range tr.Outbound() {
			fmt.Printf("  bot -> %s: %s\n", m.To, m.Text)
		}
	}()

	fmt.Println("owgbot dev REPL — you are", DevUser, "(admin). Type a message; Ctrl-D quits.")
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("you> ")
		if !sc.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		tr.Inject(DevUser, line)
	}
}
