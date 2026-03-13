package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "relaylink is disabled: legacy JSON client path removed in Gossip V1 Phase 3")
	os.Exit(2)
}
