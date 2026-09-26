package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CLIPoster places postings by running the jobpost tool (tools/jobpost): the
// request goes in on stdin as JSON, the answer comes back on stdout as JSON,
// and the tool drives a real browser in between. The command is whatever
// JOBPOST_CMD says, split on spaces, so a deployment can point it at a
// checked-out tool, a container, or a wrapper that supplies credentials.
type CLIPoster struct {
	Command string
	// Timeout bounds one posting, browser start-up included.
	Timeout time.Duration
	// Env is appended to the worker's own environment: board credentials
	// and the demo board's address.
	Env []string
}

// DefaultPostTimeout is how long a posting may take before it is a failure.
const DefaultPostTimeout = 3 * time.Minute

// NewCLIPoster returns nil for an empty command, which the handler reads as
// "not configured".
func NewCLIPoster(command string, env ...string) *CLIPoster {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	return &CLIPoster{Command: command, Timeout: DefaultPostTimeout, Env: env}
}

func (c *CLIPoster) Post(ctx context.Context, req PostRequest) (PostResult, error) {
	parts := strings.Fields(c.Command)
	if len(parts) == 0 {
		return PostResult{}, ErrNoPoster
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultPostTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return PostResult{}, err
	}
	cmd := exec.CommandContext(ctx, parts[0], append(parts[1:], "post")...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), c.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	// The tool answers on stdout whether it succeeded or not; its stderr is
	// the browser's chatter, kept for the error message only.
	var answer struct {
		PostResult
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &answer); err != nil {
		if runErr != nil {
			return PostResult{}, fmt.Errorf("jobpost tool: %w: %s", runErr, tailLine(stderr.String()))
		}
		return PostResult{}, fmt.Errorf("jobpost tool answered with something other than JSON: %s", tailLine(stdout.String()))
	}
	if answer.Error != "" {
		return PostResult{}, errors.New(answer.Error)
	}
	if runErr != nil {
		return PostResult{}, fmt.Errorf("jobpost tool: %w", runErr)
	}
	if answer.URL == "" {
		return PostResult{}, errors.New("jobpost tool reported no URL for the posting")
	}
	return answer.PostResult, nil
}
