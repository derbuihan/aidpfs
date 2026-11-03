package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"aidpfs/internal/aidp"
	"aidpfs/internal/backend"
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
	writeEnabled := flag.Bool("write", false, "enable single-writer FILE writes, limited rename and unlink")
	recoveryDir := flag.String("recovery-dir", "", "existing private recovery directory (required with --write)")
	flag.Parse()
	if *showVersion {
		fmt.Println("aidpfs", version)
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *writeEnabled != (*recoveryDir != "") {
		return fmt.Errorf("--write and --recovery-dir must be specified together")
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
	var writeMount *mount.ExperimentalMount
	var journal *recovery.Store
	if *writeEnabled {
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
		mutate := func(ctx context.Context, operation, source, destination string) error {
			return journalMutation(ctx, store, c, operation, source, destination, func(ctx context.Context, op, src, dst string) error {
				return verifiedMutation(ctx, r, op, src, dst)
			})
		}
		writeMount, err = mount.MountWithMutations(context.Background(), c.MountPoint, r, func(ctx context.Context, rel string) (*writeback.Buffer, error) {
			return r.OpenExperimentalBuffer(ctx, rel, c.Region, store)
		}, func(ctx context.Context, rel string) error {
			return journalCreate(ctx, store, c, rel, "FILE", r.CreateEmptyExperimental)
		}, func(ctx context.Context, rel string) error {
			return journalCreate(ctx, store, c, rel, "FOLDER", r.CreateDirectoryExperimental)
		}, mutate)
		if err == nil {
			server = writeMount.Server
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
	if writeMount != nil {
		writeMount.Drain()
		return checkRecoveryAfterUnmount(writeMount.Pending(), journal)
	}
	return nil
}

type mutationReader interface {
	ReadFile(context.Context, string) ([]byte, error)
	ListedKind(context.Context, string) (backend.Kind, error)
	MutateFile(context.Context, string, string, string) error
}

func verifiedMutation(ctx context.Context, r mutationReader, op, src, dst string) error {
	var before []byte
	var err error
	if op == "rename" {
		before, err = r.ReadFile(ctx, src)
		if err != nil {
			return err
		}
	}
	if err = r.MutateFile(ctx, op, src, dst); err != nil {
		return err
	}
	if _, err = r.ListedKind(ctx, src); !errors.Is(err, syscall.ENOENT) {
		return aidp.ErrOutcomeUnknown
	}
	if op == "rename" {
		kind, e := r.ListedKind(ctx, dst)
		if e != nil || kind != backend.File {
			return aidp.ErrOutcomeUnknown
		}
		after, e := r.ReadFile(ctx, dst)
		if e != nil || !bytes.Equal(before, after) {
			return aidp.ErrOutcomeUnknown
		}
	}
	return nil
}

func journalMutation(ctx context.Context, store *recovery.Store, c config.Config, operation, source, destination string, mutate func(context.Context, string, string, string) error) error {
	remote, err := config.Resolve(c.RemotePath, source)
	if err != nil {
		return err
	}
	target := ""
	if destination != "" {
		target, err = config.Resolve(c.RemotePath, destination)
		if err != nil {
			return err
		}
	}
	id, err := store.BeginMutation(c.PlatformID, c.WorkspaceKey, operation, remote, target)
	if err != nil {
		return err
	}
	if err = mutate(ctx, operation, source, destination); err != nil {
		return err
	}
	return store.ConfirmMutation(id)
}

func journalCreate(ctx context.Context, store *recovery.Store, c config.Config, rel, kind string, create func(context.Context, string) error) error {
	remote, err := config.Resolve(c.RemotePath, rel)
	if err != nil {
		return err
	}
	intentID, err := store.BeginCreateKind(c.PlatformID, c.WorkspaceKey, remote, kind)
	if err != nil {
		return err
	}
	if err = create(ctx, rel); err != nil {
		return err
	}
	return store.ConfirmCreate(intentID)
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
	mutations, err := journal.ListMutations()
	if err != nil {
		return fmt.Errorf("cannot inspect mutation intents after unmount: %w", err)
	}
	for _, intent := range mutations {
		if intent.Invalid || !intent.Confirmed {
			unknown++
		}
	}
	if pending != 0 || unknown != 0 || len(unresolved) != 0 {
		return fmt.Errorf("recovery required: %d unsaved file buffers, %d unresolved snapshots, %d unconfirmed or invalid operation intents; inspect --recovery-dir", pending, len(unresolved), unknown)
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
