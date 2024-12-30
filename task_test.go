package sup

import (
	"bytes"
	"testing"
)

func TestStackup_createTasks(t *testing.T) {
	tests := []struct {
		name     string
		command  *Command
		wantErr  bool
		wantTask bool
		local    bool
	}{
		{
			name: "local command",
			command: &Command{
				Name:  "test",
				Local: "echo 'hello'",
			},
			wantErr:  false,
			wantTask: true,
			local:    true,
		},
		{
			name: "remote command",
			command: &Command{
				Name: "test",
				Run:  "echo 'hello'",
			},
			wantErr:  false,
			wantTask: true,
			local:    false,
		},
		{
			name: "local and remote command",
			command: &Command{
				Name:  "test",
				Local: "echo 'local'",
				Run:   "echo 'remote'",
			},
			wantErr:  false,
			wantTask: true,
			local:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sup := &Stackup{
				conf: &Supfile{
					Version: "0.5",
				},
			}

			clients := []Client{
				&mockClient{
					stdout: bytes.NewBuffer([]byte("mock output")),
					stderr: bytes.NewBuffer(nil),
				},
			}

			tasks, err := sup.createTasks(tt.command, clients, "")
			if (err != nil) != tt.wantErr {
				t.Errorf("Stackup.createTasks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantTask && len(tasks) == 0 {
				t.Error("Stackup.createTasks() expected tasks but got none")
				return
			}

			if tt.local {
				found := false
				for _, task := range tasks {
					for _, client := range task.Clients {
						if _, ok := client.(*LocalhostClient); ok {
							found = true
							break
						}
					}
				}
				if !found {
					t.Error("Stackup.createTasks() expected LocalhostClient but found none")
				}
			}
		})
	}
}

func TestStackup_createTasks_WithEnv(t *testing.T) {
	sup := &Stackup{
		conf: &Supfile{
			Version: "0.5",
		},
	}

	command := &Command{
		Name:  "test",
		Local: "echo $TEST_VAR",
	}

	clients := []Client{
		&mockClient{
			stdout: bytes.NewBuffer([]byte("mock output")),
			stderr: bytes.NewBuffer(nil),
		},
	}

	env := "export TEST_VAR=test_value;"
	tasks, err := sup.createTasks(command, clients, env)
	if err != nil {
		t.Fatalf("Stackup.createTasks() error = %v", err)
	}

	if len(tasks) == 0 {
		t.Fatal("Stackup.createTasks() expected tasks but got none")
	}

	task := tasks[0]
	if len(task.Clients) != 1 {
		t.Fatal("Expected exactly one client")
	}

	client, ok := task.Clients[0].(*LocalhostClient)
	if !ok {
		t.Fatal("Expected LocalhostClient")
	}

	if client.env != env+`export SUP_HOST="localhost";` {
		t.Errorf("Expected env %q, got %q", env+`export SUP_HOST="localhost";`, client.env)
	}
}
