// pooltune-owner performs offline recovery under the local control directory lock.
// It never opens the hosted gateway accounting database or exposes a reset API.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"os"
	"path/filepath"
	"pooltune/internal/durable"
	"pooltune/internal/secrets"
	"pooltune/internal/store"
	"strings"
	"time"
)

func main() {
	if e := run(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "reset-password" && args[0] != "replace-claim") {
		return errors.New("usage: pooltune-owner reset-password|replace-claim -control-directory path")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("control-directory", "", "existing private control state directory")
	root := flags.String("secret-root", "", "private password file root")
	password := flags.String("password-file", "", "private new password file")
	claim := flags.String("claim-file", "", "new private claim token file")
	valid := flags.Duration("valid-for", time.Hour, "new claim lifetime, at most 24h")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*dir) {
		return errors.New("invalid offline owner arguments")
	}
	if args[0] == "reset-password" && (*claim != "" || *root == "" || *password == "") {
		return errors.New("password recovery requires secret-root and password-file")
	}
	if args[0] == "replace-claim" && (*root != "" || *password != "" || !filepath.IsAbs(*claim) || *valid <= 0 || *valid > 24*time.Hour) {
		return errors.New("claim replacement requires a new absolute claim-file and bounded lifetime")
	}
	// The running Control owns this same lock. Only the filesystem owner can stop
	// control and acquire it; network credentials cannot authorize offline recovery.
	owner, e := durable.Open(*dir)
	if e != nil {
		return errors.New("control directory unavailable or still owned; stop control before recovery")
	}
	defer owner.Close()
	path := filepath.Join(*dir, "control.db")
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("existing private control database required")
	}
	db, e := store.OpenExistingControlStore(path)
	if e != nil {
		return errors.New("control database unavailable")
	}
	defer db.Close()
	if args[0] == "reset-password" {
		files, e := secrets.Open(*root)
		if e != nil {
			return errors.New("private password root unavailable")
		}
		defer files.Close()
		raw, e := files.Read(*password)
		if e != nil {
			return errors.New("private password file unavailable")
		}
		text := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if len(text) < 12 || len(text) > 72 || strings.ContainsAny(text, "\r\n") {
			return errors.New("password must be one line with 12 to 72 bytes")
		}
		hash, e := bcrypt.GenerateFromPassword([]byte(text), 12)
		if e != nil {
			return errors.New("password hashing failed")
		}
		if e = db.RecoverAdminPassword(hash); e != nil {
			return errors.New("password recovery rejected; installation must already be claimed and audit writable")
		}
		fmt.Fprintln(out, "Owner password recovered and all sessions revoked. Restart control; protect or remove the input password file.")
		return nil
	}
	parent, e := os.Lstat(filepath.Dir(*claim))
	if e != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return errors.New("claim directory must be private (0700)")
	}
	files, e := os.OpenRoot(filepath.Dir(*claim))
	if e != nil {
		return errors.New("claim directory unavailable")
	}
	defer files.Close()
	name := filepath.Base(*claim)
	file, e := files.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return errors.New("claim file must not already exist")
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			files.Remove(name)
		}
	}()
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		return errors.New("claim generation failed")
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	if _, e = file.WriteString(token + "\n"); e != nil {
		return errors.New("claim persistence failed")
	}
	if e = file.Sync(); e != nil {
		return errors.New("claim persistence failed")
	}
	if e = file.Close(); e != nil {
		return errors.New("claim persistence failed")
	}
	// Sync the directory before committing its token hash. After a crash the
	// recorded replacement must have a durable private file available to its owner.
	parentFile, e := files.Open(".")
	if e != nil {
		return errors.New("claim directory persistence failed")
	}
	e = parentFile.Sync()
	parentFile.Close()
	if e != nil {
		return errors.New("claim directory persistence failed")
	}
	hash := sha256.Sum256([]byte(token))
	if e = db.ReplaceAdminClaim(hash[:], time.Now().Add(*valid)); e != nil {
		return errors.New("claim replacement rejected; installation must be unclaimed and audit writable")
	}
	complete = true
	fmt.Fprintln(out, "New claim saved privately. Set the control profile ClaimFile to the supplied file and restart control. The previous claim is invalid.")
	return nil
}
