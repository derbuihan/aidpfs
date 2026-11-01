package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"aidpfs/internal/aidp"
	"aidpfs/internal/config"
	"aidpfs/internal/mount"
	"aidpfs/internal/recovery"
	"aidpfs/internal/writeback"
	"github.com/hanwen/go-fuse/v2/fuse"
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
	experimentalWrite := flag.Bool("experimental-write", false, "enable incomplete file writes; use isolated test data only")
	recoveryDir := flag.String("recovery-dir", "", "existing private recovery directory (required for experimental writes)")
	flag.Parse()
	if *showVersion {
		fmt.Println("aidpfs", version)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *experimentalWrite != (*recoveryDir != "") {
		return fmt.Errorf("--experimental-write and --recovery-dir must be specified together")
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
	var server *fuse.Server
	var experimental *mount.ExperimentalMount
	var journal *recovery.Store
	if *experimentalWrite {
		store, openErr := recovery.Open(*recoveryDir)
		if openErr != nil {
			return fmt.Errorf("recovery directory: %w", openErr)
		}
		lock, lockErr := store.LockEmpty()
		if lockErr != nil {
			return fmt.Errorf("recovery directory: %w", lockErr)
		}
		defer lock.Close()
		journal = store
		fmt.Fprintln(os.Stderr, "WARNING: experimental writes are incomplete; use isolated test data only. Recovery entries must be reviewed manually.")
		journalCreate := func(ctx context.Context, rel, kind string, create func(context.Context, string) error) error {
			remote, resolveErr := config.Resolve(c.RemotePath, rel)
			if resolveErr != nil {
				return resolveErr
			}
			intentID, intentErr := store.BeginCreateKind(c.PlatformID, c.WorkspaceKey, remote, kind)
			if intentErr != nil {
				return intentErr
			}
			if createErr := create(ctx, rel); createErr != nil {
				return createErr
			}
			return store.ConfirmCreate(intentID)
		}
		experimental, err = mount.MountExperimental(context.Background(), c.MountPoint, r, func(ctx context.Context, rel string) (*writeback.Buffer, error) {
			return r.OpenExperimentalBuffer(ctx, rel, c.Region, store)
		}, func(ctx context.Context, rel string) error {
			return journalCreate(ctx, rel, "FILE", r.CreateEmptyExperimental)
		}, func(ctx context.Context, rel string) error {
			return journalCreate(ctx, rel, "FOLDER", r.CreateDirectoryExperimental)
		})
		if err == nil {
			server = experimental.Server
		}
	} else {
		server, err = mount.Mount(context.Background(), c.MountPoint, r)
	}
	if err != nil {
		return err
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(done)
	waitForUnmount(server, done, os.Stderr)
	if experimental != nil {
		return checkRecoveryAfterUnmount(experimental.Pending(), journal)
	}
	return nil
}

type mountServer interface {
	Wait()
	Unmount() error
}

// A failed unmount leaves the server running and permits another signal to retry.
func waitForUnmount(server mountServer, signals <-chan os.Signal, out io.Writer) {
	stopped := make(chan struct{})
	go func() { server.Wait(); close(stopped) }()
	for {
		select {
		case <-stopped:
			return
		case <-signals:
			if err := server.Unmount(); err != nil {
				fmt.Fprintf(out, "aidpfs: unmount failed: %v; close users of the mount and retry\n", err)
			}
		}
	}
}

func checkRecoveryAfterUnmount(pending int, journal *recovery.Store) error {
	unresolved, err := journal.UnresolvedSnapshots()
	if err != nil {
		return fmt.Errorf("cannot inspect recovery snapshots after unmount: %w", err)
	}
	intents, err := journal.ListCreates()
	if err != nil {
		return fmt.Errorf("cannot inspect recovery directory after unmount: %w", err)
	}
	unknown := 0
	for _, intent := range intents {
		if intent.Invalid || !intent.Confirmed {
			unknown++
		}
	}
	if pending != 0 || unknown != 0 || len(unresolved) != 0 {
		return fmt.Errorf("recovery required: %d unsaved file buffers, %d unresolved snapshots, %d unconfirmed or invalid create intents; inspect --recovery-dir", pending, len(unresolved), unknown)
	}
	return nil
}
func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "recovery" {
		err = runRecovery(os.Args[2:], os.Stdout)
	} else {
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "aidpfs:", err)
		os.Exit(1)
	}
}
