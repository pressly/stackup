package sup

import (
	"bytes"
	"io"
	"os"
	"testing"
)

type mockClient struct {
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
	pty    bool
}

func (c *mockClient) Connect() error {
	return nil
}

func (c *mockClient) Run(task *Task) error {
	if task.TTY {
		c.pty = true
	}
	if task.Input != nil {
		c.stdin = &mockWriteCloser{Buffer: bytes.NewBuffer(nil)}
		_, err := io.Copy(c.stdin, task.Input)
		if err != nil {
			return err
		}
		c.stdin.(*mockWriteCloser).Close()
	}
	return nil
}

func (c *mockClient) Wait() error {
	return nil
}

func (c *mockClient) Close() error {
	return nil
}

func (c *mockClient) Prefix() (string, int) {
	return "mock", 4
}

func (c *mockClient) Write(p []byte) (n int, err error) {
	if c.stdin != nil {
		return c.stdin.Write(p)
	}
	return len(p), nil
}

func (c *mockClient) WriteClose() error {
	if c.stdin != nil {
		return c.stdin.Close()
	}
	return nil
}

func (c *mockClient) Stdin() io.WriteCloser {
	return c.stdin
}

func (c *mockClient) Stderr() io.Reader {
	return c.stderr
}

func (c *mockClient) Stdout() io.Reader {
	return c.stdout
}

func (c *mockClient) Signal(sig os.Signal) error {
	return nil
}

type mockWriteCloser struct {
	*bytes.Buffer
	closed bool
}

func (m *mockWriteCloser) Close() error {
	m.closed = true
	return nil
}

func TestClient_Run(t *testing.T) {
	tests := []struct {
		name        string
		task        *Task
		wantErr     bool
		wantPTY     bool
		wantInput   string
		interactive bool
	}{
		{
			name: "non-interactive command",
			task: &Task{
				Run:   "echo 'hello'",
				TTY:   false,
				Input: nil,
			},
			wantErr:     false,
			wantPTY:     false,
			interactive: false,
		},
		{
			name: "interactive command",
			task: &Task{
				Run:   "bash",
				TTY:   true,
				Input: nil,
			},
			wantErr:     false,
			wantPTY:     true,
			interactive: true,
		},
		{
			name: "command with input",
			task: &Task{
				Run:   "cat",
				TTY:   false,
				Input: bytes.NewBufferString("test input"),
			},
			wantErr:     false,
			wantPTY:     false,
			wantInput:   "test input",
			interactive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockClient{
				stdout: bytes.NewBuffer([]byte("mock output")),
				stderr: bytes.NewBuffer(nil),
			}

			if err := client.Run(tt.task); (err != nil) != tt.wantErr {
				t.Errorf("Client.Run() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.interactive && !client.pty {
				t.Error("Expected PTY to be requested for interactive command")
			}

			if tt.wantInput != "" {
				input := client.stdin.(*mockWriteCloser)
				if input.String() != tt.wantInput {
					t.Errorf("Expected input %q, got %q", tt.wantInput, input.String())
				}
				if !input.closed {
					t.Error("Expected input to be closed")
				}
			}
		})
	}
}
