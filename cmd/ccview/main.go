// Command ccview is a compact console monitor for Claude usage limits.
//
// See the package documentation in internal/cli and the project README for
// usage details. This entry point simply delegates to cli.Execute.
package main

import "github.com/merlindeep/claude-cost-viewer/internal/cli"

func main() {
	cli.Execute()
}
