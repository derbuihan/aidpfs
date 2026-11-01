package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"aidpfs/internal/recovery"
)

func runRecovery(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: aidpfs recovery list|export [flags]")
	}
	command := args[0]
	fs := flag.NewFlagSet("recovery "+command, flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "absolute recovery directory")
	id := fs.String("id", "", "recovery entry ID (export)")
	output := fs.String("output", "", "new absolute destination path (export)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	store, err := recovery.Open(*dir)
	if err != nil {
		return err
	}
	switch command {
	case "list":
		if *id != "" || *output != "" {
			return errors.New("list accepts only --dir")
		}
		entries, err := store.List()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Invalid {
				fmt.Fprintf(out, "%s incomplete-or-invalid\n", e.ID)
			} else {
				fmt.Fprintf(out, "%s %s %d bytes generation=%d previous=%s\n", e.ID, e.Stage, e.Size, e.Generation, e.PreviousID)
			}
		}
		return nil
	case "export":
		if *id == "" || *output == "" {
			return errors.New("export requires --id and --output")
		}
		if err = store.Export(*id, *output); err != nil {
			return err
		}
		fmt.Fprintln(out, "Recovery content exported; original journal retained.")
		return nil
	default:
		return errors.New("unknown recovery operation")
	}
}
