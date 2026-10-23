package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxSockPath is the longest socket path: sun_path on macOS is 104 bytes
// with the closing NUL.
const maxSockPath = 103

// Paths are the files of one holder.
type Paths struct {
	Sock string
	Lock string
	// Log is the log of a standalone holder. Empty in Trebi mode, where
	// serve logs to stderr.
	Log string
}

// StandalonePaths are the files of the holder for key in dir/run.
func StandalonePaths(dir, key string) (Paths, error) {
	sum := sha256.Sum256([]byte(key))
	base := filepath.Join(dir, "run", hex.EncodeToString(sum[:])[:12])
	return checked(Paths{Sock: base + ".sock", Lock: base + ".lock", Log: base + ".log"})
}

// TrebiPaths are the files of serve in the state folder.
func TrebiPaths(stateDir string) (Paths, error) {
	return checked(Paths{Sock: filepath.Join(stateDir, "vnc.sock"), Lock: filepath.Join(stateDir, "serve.lock")})
}

func checked(p Paths) (Paths, error) {
	if len(p.Sock) > maxSockPath {
		return p, fmt.Errorf("the session socket path %s is %d bytes, more than %d: use a shorter home folder or --session off", p.Sock, len(p.Sock), maxSockPath)
	}
	return p, nil
}

// ErrBusy is a lock that another holder has.
var ErrBusy = errors.New("another session holds the lock")

// Lock takes the lock file. Close the file to release it.
func Lock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

// Listen removes a stale socket and listens on path with mode 0600. Call
// it only with the lock.
func Listen(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	return ln, err
}

// maxLog is the log size at which a new holder starts the log again.
const maxLog = 1 << 20

func openLog(path string) (*os.File, error) {
	flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > maxLog {
		flag |= os.O_TRUNC
	}
	return os.OpenFile(path, flag, 0o600)
}

// lastLine is the last line of the log, for an error message.
func lastLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 4096 {
		f.Seek(-4096, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}
