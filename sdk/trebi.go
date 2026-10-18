package sdk

import (
	"errors"
	"os"
	"path/filepath"
)

// Env names of the folder contract.
const (
	EnvStateDir = "TREBI_STATE_DIR"
	EnvCacheDir = "TREBI_CACHE_DIR"
)

// Trebi is the folder contract that the daemon gives to a program.
type Trebi struct {
	StateDir string // TREBI_STATE_DIR: durable data
	CacheDir string // TREBI_CACHE_DIR: data that the program can build again
}

// FromEnv reads the contract. ok is false outside Trebi.
func FromEnv() (t Trebi, ok bool) {
	t = Trebi{StateDir: os.Getenv(EnvStateDir), CacheDir: os.Getenv(EnvCacheDir)}
	return t, t.StateDir != ""
}

// WriteFileAtomic writes b to path through the temp file path+".tmp", a
// sync, and a rename. A crash leaves the old file or the new file, never
// part of a file. Do not write one path from two goroutines at a time.
func WriteFileAtomic(path string, b []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync() // not all file systems sync a folder
		_ = d.Close()
	}
	return nil
}

// FolderUser gets the folders of the session before initialize. Serve
// calls UseFolders with the values of WithStateDir and WithCacheDir, or of
// the env.
type FolderUser interface {
	UseFolders(t Trebi) error
}

// MissingInput is the error a program returns when a required input is
// not set in Trebi mode. Serve reports it as status auth_required with
// reason missing_input and the input name in the message.
type MissingInput struct{ Name, Label string }

func (e MissingInput) Error() string {
	if e.Label == "" {
		return e.Name + " is missing"
	}
	return e.Label + " (" + e.Name + ") is missing"
}

// asMissingInput finds a MissingInput value or pointer in err.
func asMissingInput(err error) (MissingInput, bool) {
	var v MissingInput
	if errors.As(err, &v) {
		return v, true
	}
	var p *MissingInput
	if errors.As(err, &p) && p != nil {
		return *p, true
	}
	return MissingInput{}, false
}
