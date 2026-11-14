// pooltune-admin is an owner-only local command. There is no public approval API.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"pooltune/internal/gateway"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("pooltune-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	db := flags.String("db", "", "absolute hosted gateway database path")
	action := flags.String("action", "", "approve, rotate or revoke")
	account := flags.String("account", "", "account ID")
	cap := flags.Int64("global-micros", 0, "global lifetime USD cap in millionths of a dollar")
	allowance := flags.Int64("allowance-micros", 0, "account lifetime allowance")
	tokenFile := flags.String("token-file", "", "new private installation token file")
	validFor := flags.Duration("valid-for", 24*time.Hour, "installation token lifetime")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*action != "approve" && *action != "rotate" && *action != "revoke") {
		return errors.New("provide action, absolute db path, account and explicit global cap")
	}
	s, err := gateway.Open(*db, gateway.Limits{GlobalMicros: *cap, MaxConcurrent: 4, MaxAccountConcurrent: 1, RequestsPerMinute: 30, MaxRunSteps: 8, RunTTL: 5 * time.Minute})
	if err != nil {
		return errors.New("cannot open hosted gateway accounting with supplied limits")
	}
	defer s.Close()
	if *action == "revoke" {
		if s.Revoke(*account) != nil {
			return errors.New("account revocation failed")
		}
		fmt.Fprintln(out, "Account revoked. Already dispatched calls may still incur their reserved cost.")
		return nil
	}
	if !filepath.IsAbs(*tokenFile) || *validFor <= 0 || *validFor > 366*24*time.Hour {
		return errors.New("provide an absolute new token file and bounded lifetime")
	}
	parent, err := os.Lstat(filepath.Dir(*tokenFile))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return errors.New("token directory must be private (0700)")
	}
	root, err := os.OpenRoot(filepath.Dir(*tokenFile))
	if err != nil {
		return errors.New("cannot open token directory")
	}
	defer root.Close()
	name := filepath.Base(*tokenFile)
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("token file must not already exist")
	}
	success := false
	defer func() {
		file.Close()
		if !success {
			root.Remove(name)
		}
	}()
	var token string
	if *action == "approve" {
		token, err = s.Approve(*account, *allowance, time.Now().Add(*validFor))
	} else {
		token, err = s.Rotate(*account, time.Now().Add(*validFor))
	}
	if err != nil {
		return errors.New("account operation failed")
	}
	if _, err = file.WriteString(token + "\n"); err != nil {
		s.Revoke(*account)
		return errors.New("token file write failed; account revoked")
	}
	if err = file.Sync(); err != nil {
		s.Revoke(*account)
		return errors.New("token persistence failed; account revoked")
	}
	if err = file.Close(); err != nil {
		s.Revoke(*account)
		return errors.New("token file close failed; account revoked")
	}
	success = true
	fmt.Fprintln(out, "Installation token saved to the supplied private file. Provider credentials remain on the hosted gateway.")
	return nil
}
