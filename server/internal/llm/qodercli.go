package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// QoderCLI drives the local Qoder CLI in headless mode. One process per turn,
// no session persistence: the engine owns the conversation, so an agent can
// only ever see the context the engine assembled for it.
type QoderCLI struct {
	Bin        string
	Model      string
	Permission string
	WorkDir    string
	Timeout    time.Duration

	once sync.Once
	err  error
}

// NewQoderCLI builds the provider. workDir is the scratch directory the child
// process runs in; it holds no project files, so there is nothing to read.
func NewQoderCLI(bin, model, permission, workDir string, timeout time.Duration) *QoderCLI {
	if bin == "" {
		bin = "qodercli"
	}
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	return &QoderCLI{Bin: bin, Model: model, Permission: permission, WorkDir: workDir, Timeout: timeout}
}

// Name implements Provider.
func (q *QoderCLI) Name() string { return "qodercli" }

func (q *QoderCLI) ensureDir() error {
	q.once.Do(func() {
		dir := q.WorkDir
		if dir == "" {
			dir = filepath.Join(os.TempDir(), "script-play-agent")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			q.err = err
			return
		}
		q.WorkDir = dir
	})
	return q.err
}

// Stream implements Provider.
func (q *QoderCLI) Stream(ctx context.Context, req Request, sink func(string) error) error {
	if err := q.ensureDir(); err != nil {
		return err
	}
	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}
	prompt := FlattenMessages(req.Messages)
	written := false
	track := func(delta string) error {
		if delta == "" {
			return nil
		}
		written = true
		if sink != nil {
			return sink(delta)
		}
		return nil
	}

	out, err := q.run(ctx, "stream-json", prompt, req, track)
	if err == nil && written && strings.TrimSpace(out) != "" {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Fallback: some CLI builds only emit the final object.
	if strings.TrimSpace(out) == "" || !written {
		final, err2 := q.run(ctx, "json", prompt, req, nil)
		if err2 != nil {
			if err != nil {
				return err
			}
			return err2
		}
		if strings.TrimSpace(final) != "" && sink != nil {
			return sink(final)
		}
		return nil
	}
	return err
}

func (q *QoderCLI) args(format string, req Request) []string {
	args := []string{
		"-p",
		"-o", format,
		"--no-session-persistence",
	}
	// --tools= is not decoration: without it the CLI would hand the agent
	// Read/Bash and it could walk the repository, which breaks information
	// isolation for every seat.
	args = append(args, "--tools=")
	if q.Permission != "" {
		args = append(args, "--permission-mode", q.Permission)
	}
	model := req.Model
	if model == "" {
		model = q.Model
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	if strings.TrimSpace(req.System) != "" {
		args = append(args, "--system-prompt", req.System)
	}
	for _, img := range req.Images {
		args = append(args, "--attachment", img.Path)
	}
	return args
}

type qoderAssistantEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Message *struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// run executes the CLI once and returns the final text (streamed through sink
// when non-nil).
func (q *QoderCLI) run(ctx context.Context, format, prompt string, req Request, sink func(string) error) (string, error) {
	cmd := exec.CommandContext(ctx, q.Bin, q.args(format, req)...)
	cmd.Dir = q.WorkDir
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("qodercli start: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 800 {
			msg = msg[:800] + "..."
		}
		return "", fmt.Errorf("qodercli exited: %w (%s)", err, msg)
	}

	if format == "json" {
		return parseQoderJSON(stdout.Bytes())
	}
	return parseQoderStream(stdout.Bytes(), sink)
}

func parseQoderJSON(b []byte) (string, error) {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev qoderAssistantEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Type != "result" {
			continue
		}
		if ev.IsError {
			return "", fmt.Errorf("qodercli error: %s", ev.Result)
		}
		return ev.Result, nil
	}
	return "", errors.New("qodercli: no result object in output")
}

func parseQoderStream(b []byte, sink func(string) error) (string, error) {
	var (
		final   string
		emitted = map[string]int{}
		buf     strings.Builder
	)
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev qoderAssistantEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			if ev.Message == nil {
				continue
			}
			var text strings.Builder
			for _, c := range ev.Message.Content {
				if c.Type == "text" {
					text.WriteString(c.Text)
				}
			}
			full := text.String()
			if full == "" {
				continue
			}
			key := ev.Message.ID
			done := emitted[key]
			if len(full) <= done {
				continue
			}
			delta := full[done:]
			emitted[key] = len(full)
			buf.WriteString(delta)
			if sink != nil {
				if err := sink(delta); err != nil {
					return buf.String(), err
				}
			}
		case "result":
			if ev.IsError {
				return buf.String(), fmt.Errorf("qodercli error: %s", ev.Result)
			}
			final = ev.Result
			if buf.Len() == 0 && final != "" {
				buf.WriteString(final)
				if sink != nil {
					if err := sink(final); err != nil {
						return buf.String(), err
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return buf.String(), err
	}
	if buf.Len() == 0 {
		return final, nil
	}
	return buf.String(), nil
}

// FlattenMessages renders the conversation into the single prompt the CLI
// receives on stdin.
func FlattenMessages(msgs []Message) string {
	if len(msgs) == 0 {
		return ""
	}
	if len(msgs) == 1 {
		return msgs[0].Text
	}
	var b strings.Builder
	for _, m := range msgs {
		role := m.Role
		switch role {
		case "assistant":
			role = "你之前说过"
		case "system":
			role = "系统"
		default:
			role = "当前信息"
		}
		fmt.Fprintf(&b, "【%s】\n%s\n\n", role, strings.TrimSpace(m.Text))
	}
	return strings.TrimSpace(b.String())
}
