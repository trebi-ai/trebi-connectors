package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Dial connects to the daemon socket for storeDir. A non-nil error means no
// live daemon is accepting connections (no socket, stale socket, or refused).
func Dial(storeDir string) (net.Conn, error) {
	return net.DialTimeout("unix", SocketPath(storeDir), 2*time.Second)
}

// WriteRequest writes a single request line to conn without closing it or
// reading a reply. Used for the long-lived "subscribe" stream, where the caller
// keeps reading event lines off conn afterwards.
func WriteRequest(conn net.Conn, req Request) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	return nil
}

// Send forwards a request to a running daemon and returns its response. The
// caller is expected to have obtained conn from Dial. conn is closed on return.
func Send(conn net.Conn, req Request) (Response, error) {
	defer conn.Close()

	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	b = append(b, '\n')

	// Sending a message (esp. media upload) can take a while.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	if _, err := conn.Write(b); err != nil {
		return Response{}, fmt.Errorf("write request: %w", err)
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}
