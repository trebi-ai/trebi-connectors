// Package fleet runs the built vnc-cli binary against real servers: the
// Docker fleet of docker-compose.yml (tag fleet) and the computers of
// VNC_LIVE_SERVERS (tag live). Each command runs in a child process, so a
// hang stays in the child. The package has only tests.
package fleet
