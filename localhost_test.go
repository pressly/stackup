package sup

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestLocalhostClient_Run(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}

	// Verify that basic shell commands are available
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available in PATH")
	}

	tests := []struct {
		name        string
		task        *Task
		env         string
		wantErr     bool
		wantOutput  string
		interactive bool
	}{
		{
			name: "simple command",
			task: &Task{
				Run:   "printf 'hello\\n'",
				TTY:   false,
				Input: nil,
			},
			wantErr:     false,
			wantOutput:  "hello\n",
			interactive: false,
		},
		{
			name: "command with input",
			task: &Task{
				Run:   "cat",
				TTY:   false,
				Input: bytes.NewBufferString("test input"),
			},
			wantErr:     false,
			wantOutput:  "test input",
			interactive: false,
		},
		{
			name: "command with environment variables",
			task: &Task{
				Run:   "printf \"$TEST_VAR\\n\"",
				TTY:   false,
				Input: nil,
			},
			env:         "export TEST_VAR=test_value;",
			wantErr:     false,
			wantOutput:  "test_value\n",
			interactive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &LocalhostClient{
				env: tt.env,
			}

			if err := client.Connect(); err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}

			if err := client.Run(tt.task); (err != nil) != tt.wantErr {
				t.Errorf("LocalhostClient.Run() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.interactive && tt.wantOutput != "" {
				var output []byte
				var err error

				// Use a channel to handle timeout
				done := make(chan bool)
				go func() {
					output, err = io.ReadAll(client.stdout)
					done <- true
				}()

				// Wait with timeout
				select {
				case <-done:
					if err != nil {
						t.Fatalf("Failed to read output: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("Timeout waiting for command output")
				}

				if string(output) != tt.wantOutput {
					t.Errorf("LocalhostClient.Run() output = %q, want %q", string(output), tt.wantOutput)
				}
			}

			if err := client.Wait(); err != nil && !tt.wantErr {
				t.Errorf("LocalhostClient.Wait() error = %v", err)
			}
		})
	}
}

func TestLocalhostClient_Signal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}

	client := &LocalhostClient{}
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	task := &Task{
		Run:   "sleep 10",
		TTY:   false,
		Input: nil,
	}

	if err := client.Run(task); err != nil {
		t.Fatalf("Failed to run task: %v", err)
	}

	// Give the process time to start
	time.Sleep(100 * time.Millisecond)

	// Send interrupt signal
	if err := client.Signal(os.Interrupt); err != nil {
		t.Errorf("LocalhostClient.Signal() error = %v", err)
	}

	// Wait should return with an error due to the interrupt
	err := client.Wait()
	if err == nil {
		t.Error("Expected error from Wait() after interrupt")
	}
}
