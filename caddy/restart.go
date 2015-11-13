// +build !windows

package caddy

import (
	"encoding/gob"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
)

func init() {
	gob.Register(CaddyfileInput{})
}

// Restart restarts the entire application; gracefully with zero
// downtime if on a POSIX-compatible system, or forcefully if on
// Windows but with imperceptibly-short downtime.
//
// The restarted application will use newCaddyfile as its input
// configuration. If newCaddyfile is nil, the current (existing)
// Caddyfile configuration will be used.
//
// Note: The process must exist in the same place on the disk in
// order for this to work. Thus, multiple graceful restarts don't
// work if executing with `go run`, since the binary is cleaned up
// when `go run` sees the initial parent process exit.
func Restart(newCaddyfile Input) error {
	if newCaddyfile == nil {
		caddyfileMu.Lock()
		newCaddyfile = caddyfile
		caddyfileMu.Unlock()
	}

	if len(os.Args) == 0 { // this should never happen, but...
		os.Args = []string{""}
	}

	// Tell the child that it's a restart
	os.Setenv("CADDY_RESTART", "true")

	// Prepare our payload to the child process
	cdyfileGob := caddyfileGob{
		ListenerFds: make(map[string]uintptr),
		Caddyfile:   newCaddyfile,
	}

	// Prepare a pipe to the fork's stdin so it can get the Caddyfile
	rpipe, wpipe, err := os.Pipe()
	if err != nil {
		return err
	}

	// Prepare a pipe that the child process will use to communicate
	// its success with us by sending > 0 bytes
	sigrpipe, sigwpipe, err := os.Pipe()
	if err != nil {
		return err
	}

	// Pass along relevant file descriptors to child process; ordering
	// is very important since we rely on these being in certain positions.
	extraFiles := []*os.File{sigwpipe}

	// Add file descriptors of all the sockets
	serversMu.Lock()
	for i, s := range servers {
		extraFiles = append(extraFiles, s.ListenerFd())
		cdyfileGob.ListenerFds[s.Addr] = uintptr(4 + i) // 4 fds come before any of the listeners
	}
	serversMu.Unlock()

	// Set up the command
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Stdin = rpipe      // fd 0
	cmd.Stdout = os.Stdout // fd 1
	cmd.Stderr = os.Stderr // fd 2
	cmd.ExtraFiles = extraFiles

	// Spawn the child process
	err = cmd.Start()
	if err != nil {
		return err
	}

	// Feed Caddyfile to the child
	err = gob.NewEncoder(wpipe).Encode(cdyfileGob)
	if err != nil {
		return err
	}
	wpipe.Close()

	// Determine whether child startup succeeded
	sigwpipe.Close() // close our copy of the write end of the pipe -- TODO: why?
	answer, readErr := ioutil.ReadAll(sigrpipe)
	if answer == nil || len(answer) == 0 {
		cmdErr := cmd.Wait() // get exit status
		log.Printf("[ERROR] Restart: child failed to initialize (%v) - changes not applied", cmdErr)
		if readErr != nil {
			log.Printf("[ERROR] Restart: additionally, error communicating with child process: %v", readErr)
		}
		return errIncompleteRestart
	}

	// Looks like child was successful; we can exit gracefully.
	return Stop()
}