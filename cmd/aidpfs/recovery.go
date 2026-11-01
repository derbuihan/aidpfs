package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"aidpfs/internal/aidp"

	"aidpfs/internal/recovery"
)

func runRecovery(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: aidpfs recovery list|lineage|export|inspect [flags]")
	}
	command := args[0]
	fs := flag.NewFlagSet("recovery "+command, flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "absolute recovery directory")
	id := fs.String("id", "", "recovery entry ID (export)")
	output := fs.String("output", "", "new absolute destination path (export)")
	profile := fs.String("profile", "DEFAULT", "OCI API-key profile (inspect)")
	region := fs.String("region", "", "OCI region (inspect)")
	endpoint := fs.String("endpoint", "", "HTTPS AIDP endpoint (inspect)")
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
		if *id != "" || *output != "" || *region != "" || *endpoint != "" || *profile != "DEFAULT" {
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
		creates, err := store.ListCreates()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		for _, item := range creates {
			if err = enc.Encode(item); err != nil {
				return err
			}
		}
		return nil
	case "lineage":
		if *id == "" || *output != "" || *region != "" || *endpoint != "" || *profile != "DEFAULT" {
			return errors.New("lineage requires --id and --dir")
		}
		entries, err := store.Lineage(*id)
		if err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Fprintf(out, "%s %s %d bytes generation=%d previous=%s\n", e.ID, e.Stage, e.Size, e.Generation, e.PreviousID)
		}
		fmt.Fprintln(out, "Ancestry verified; newer or incomplete branches may exist.")
		return nil
	case "export":
		if *id == "" || *output == "" || *region != "" || *endpoint != "" || *profile != "DEFAULT" {
			return errors.New("export requires --id and --output")
		}
		if err = store.Export(*id, *output); err != nil {
			return err
		}
		fmt.Fprintln(out, "Recovery content exported; original journal retained.")
		return nil
	case "inspect":
		if *id == "" || *region == "" || *output != "" {
			return errors.New("inspect requires --id and --region")
		}
		record, data, err := store.Load(*id)
		if err != nil {
			return err
		}
		root := path.Dir(record.RemotePath)
		if root == "/" {
			return errors.New("root-level target is not supported")
		}
		service := *endpoint
		if service == "" {
			service = "https://aidp." + *region + ".oci.oraclecloud.com"
		}
		reader, err := aidp.New(service, record.PlatformID, record.WorkspaceKey, root, aidp.APIKeySigner(os.Getenv("OCI_CONFIG_FILE"), *profile))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		observation, err := reader.InspectRecovery(ctx, *region, record, data)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "stage=%s observation=%s (point-in-time; no write replayed)\n", record.Stage, observation)
		return nil
	default:
		return errors.New("unknown recovery operation")
	}
}
