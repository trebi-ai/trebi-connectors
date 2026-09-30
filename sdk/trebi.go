package sdk

import (
	"errors"
	"os"
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
