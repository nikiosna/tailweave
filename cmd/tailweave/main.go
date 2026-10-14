// Command tailweave joins one machine to several tailnets at once.
//
// Nothing is implemented yet; see docs/PLAN.de.md and the lab in lab/.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "tailweave %s: not implemented yet\n", version)
	os.Exit(1)
}
