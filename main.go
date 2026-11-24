// warden is a plugin for the sf-compatible mode of the force CLI.  It
// provides the "warden" commands, which audit, provision and change the
// access of the users of a Salesforce org.
package main

import (
	"os"

	"github.com/ForceCLI/force/sfplugin"
	"github.com/ForceCLI/warden-plugin/cmd"
)

func main() {
	sfplugin.Main(cmd.Manifest(), func(args []string) int {
		return cmd.Run(args, os.Stdout, os.Stderr)
	})
}
