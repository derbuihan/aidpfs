package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println("aidpfs", version)
		return
	}
	fmt.Fprintln(os.Stderr, "aidpfs: mount is not implemented yet; see TASK.md")
	os.Exit(1)
}
