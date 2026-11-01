package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"aidpfs/internal/aidp"
	"aidpfs/internal/config"
	"aidpfs/internal/mount"
)

var version = "dev"

func run() error {
	var c config.Config
	flag.StringVar(&c.Profile, "profile", "DEFAULT", "OCI API key profile")
	flag.StringVar(&c.Region, "region", "", "OCI region")
	flag.StringVar(&c.Endpoint, "endpoint", "", "HTTPS AIDP endpoint (default: aidp.<region>.oci.oraclecloud.com)")
	flag.StringVar(&c.PlatformID, "platform-id", "", "AIDP platform OCID")
	flag.StringVar(&c.WorkspaceKey, "workspace-key", "", "workspace key, not display name")
	flag.StringVar(&c.RemotePath, "remote-path", "", "absolute workspace subtree")
	flag.StringVar(&c.MountPoint, "mount-point", "", "absolute local mount directory")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println("aidpfs", version)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://aidp." + c.Region + ".oci.oraclecloud.com"
		if err := c.Validate(); err != nil {
			return err
		}
	}
	r, err := aidp.New(c.Endpoint, c.PlatformID, c.WorkspaceKey, c.RemotePath, aidp.APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), c.Profile))
	if err != nil {
		return err
	}
	server, err := mount.Mount(context.Background(), c.MountPoint, r)
	if err != nil {
		return err
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(done)
	go func() { <-done; _ = server.Unmount() }()
	server.Wait()
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "aidpfs:", err)
		os.Exit(1)
	}
}
