package main

import (
	"os"

	happyusage "github.com/SunChJ/happyusage-cli"
)

func main() {
	os.Exit(happyusage.Main("hu", os.Args[1:]))
}
