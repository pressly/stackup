package sup

import (
	"fmt"
	"github.com/goware/prefixer"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
)

// Task represents a set of commands to be run.
type Task struct {
	Run     string
	Input   io.Reader
	Clients []Client
	TTY     bool
}

func (sup *Stackup) createTasks(cmd *Command, clients []Client, env string) (tasks []*Task, err error) {
	var (
		cwd             string
		uploadFile      string
		uploadTarReader io.Reader
		f               *os.File
		data            []byte
	)

	if cwd, err = os.Getwd(); err != nil {
		err = errors.Wrap(err, "resolving CWD failed")
		return
	}

	// Anything to upload?
	tasks = []*Task{}
	for _, upload := range cmd.Upload {
		if uploadFile, err = ResolveLocalPath(cwd, upload.Src, env); err != nil {
			err = errors.Wrap(err, "upload: "+upload.Src)
			return
		}

		if uploadTarReader, err = NewTarStreamReader(cwd, uploadFile, upload.Exc); err != nil {
			err = errors.Wrap(err, "upload: "+upload.Src)
			return
		}

		task := Task{
			Run:   RemoteTarCommand(upload.Dst),
			Input: uploadTarReader,
			TTY:   false,
		}

		if cmd.Once {
			task.Clients = []Client{clients[0]}
			tasks = append(tasks, &task)
			continue
		}

		if cmd.Serial > 0 {
			// Each "serial" task client group is executed sequentially.
			for i := 0; i < len(clients); i += cmd.Serial {
				j := i + cmd.Serial
				if j > len(clients) {
					j = len(clients)
				}

				copyTask := task
				copyTask.Clients = clients[i:j]
				tasks = append(tasks, &copyTask)
			}

			continue
		}

		task.Clients = clients
		tasks = append(tasks, &task)
	}

	// Script. Read the file as a multiline input command.
	if cmd.Script != "" {
		if f, err = os.Open(cmd.Script); err != nil {
			err = errors.Wrap(err, "can't open script")
			return
		}

		if data, err = ioutil.ReadAll(f); err != nil {
			err = errors.Wrap(err, "can't read script")
			return
		}

		task := Task{
			Run: string(data),
			TTY: true,
		}

		if sup.debug {
			task.Run = "set -x;" + task.Run
		}

		if cmd.Stdin {
			task.Input = os.Stdin
		}

		if cmd.Once {
			task.Clients = []Client{clients[0]}
			tasks = append(tasks, &task)

		} else if cmd.Serial > 0 {
			// Each "serial" task client group is executed sequentially.
			for i := 0; i < len(clients); i += cmd.Serial {
				j := i + cmd.Serial
				if j > len(clients) {
					j = len(clients)
				}
				copyTask := task
				copyTask.Clients = clients[i:j]
				tasks = append(tasks, &copyTask)
			}

		} else {
			task.Clients = clients
			tasks = append(tasks, &task)
		}
	}

	// Local command.
	if cmd.Local != "" {
		local := &LocalhostClient{
			env: env + `export SUP_HOST="localhost";`,
		}

		_ = local.Connect()
		task := &Task{
			Run:     cmd.Local,
			Clients: []Client{local},
			TTY:     true,
		}

		if sup.debug {
			task.Run = "set -x;" + task.Run
		}

		if cmd.Stdin {
			task.Input = os.Stdin
		}

		tasks = append(tasks, task)
	}

	// Remote command.
	if cmd.Run != "" {
		task := Task{
			Run: cmd.Run,
			TTY: true,
		}

		if sup.debug {
			task.Run = "set -x;" + task.Run
		}

		if cmd.Stdin {
			task.Input = os.Stdin
		}

		if cmd.Once {
			task.Clients = []Client{clients[0]}
			tasks = append(tasks, &task)

		} else if cmd.Serial > 0 {
			// Each "serial" task client group is executed sequentially.
			for i := 0; i < len(clients); i += cmd.Serial {
				j := i + cmd.Serial
				if j > len(clients) {
					j = len(clients)
				}
				copyTask := task
				copyTask.Clients = clients[i:j]
				tasks = append(tasks, &copyTask)
			}

		} else {
			task.Clients = clients
			tasks = append(tasks, &task)
		}
	}

	return
}
func (t *Task) formatClientPrefix(c Client, len int) string {
	p, _ := c.Prefix()
	return fmt.Sprintf("%"+strconv.Itoa(len)+"s", p)
}

func (t *Task) do(onPrefix bool, maxLen int) (err error) {
	var writers []io.Writer

	// Run tasks on the provided clients.
	wg := &sync.WaitGroup{}
	for _, c := range t.Clients {
		var prefix string
		var prefixLen int
		if onPrefix {
			prefix, prefixLen = c.Prefix()
			if len(prefix) < maxLen { // Left padding.
				prefix = strings.Repeat(" ", maxLen-prefixLen) + prefix
			}
		}

		if err = c.Run(t); err != nil {
			return errors.Wrap(err, prefix+"task failed")
		}

		// Copy over task's STDOUT.
		wg.Add(1)
		go func(c Client) {
			defer wg.Done()
			_, derr := io.Copy(os.Stdout, prefixer.New(c.Stdout(), prefix))
			if derr != nil && derr != io.EOF {
				// TODO: io.Copy() should not return io.EOF at all.
				// Upstream bug? Or prefixer.WriteTo() bug?
				_, _ = fmt.Fprintf(os.Stderr, "%v", errors.Wrap(derr, prefix+"reading STDOUT failed"))
			}
		}(c)

		// Copy over task's STDERR.
		wg.Add(1)
		go func(c Client) {
			defer wg.Done()
			_, derr := io.Copy(os.Stderr, prefixer.New(c.Stderr(), prefix))
			if derr != nil && derr != io.EOF {
				_, _ = fmt.Fprintf(os.Stderr, "%v", errors.Wrap(derr, prefix+"reading STDERR failed"))
			}
		}(c)

		writers = append(writers, c.Stdin())
	}

	// Copy over task's STDIN.
	if t.Input != nil {
		go t.copyStdin(writers)
	}

	// Catch OS signals and pass them to all active clients.
	trap := make(chan os.Signal, 1)
	signal.Notify(trap, os.Interrupt)
	go t.catchSignals(trap)

	// Wait for all I/O operations first.
	wg.Wait()

	// Make sure each client finishes the task, return on failure.
	t.clientsFinish(onPrefix, maxLen)

	// Stop catching signals for the currently active clients.
	signal.Stop(trap)
	close(trap)
	return
}

func (t *Task) catchSignals(trap chan os.Signal) {
	var err error

	for {
		select {
		case sig, ok := <-trap:
			if !ok {
				return
			}

			for _, c := range t.Clients {
				if err = c.Signal(sig); err != nil {
					_, err = fmt.Fprintf(os.Stderr, "%v", errors.Wrap(err, "sending signal failed"))
					if err != nil {
						log.Println("catchSignals Fprintf:", err)
					}
				}
			}
		}
	}
}

func (t *Task) copyStdin(writers []io.Writer) {
	var err error

	writer := io.MultiWriter(writers...)
	_, err = io.Copy(writer, t.Input)
	if err != nil && err != io.EOF {
		_, err = fmt.Fprintf(os.Stderr, "%v", errors.Wrap(err, "copying STDIN failed"))
		if err != nil {
			log.Println("copyStdin Fprintf:", err)
		}
	}

	err = nil
	for _, c := range t.Clients {
		if e := c.WriteClose(); e != nil {
			err = multierror.Append(err, e)
		}
	}

	if err != nil {
		_, err = fmt.Fprintf(os.Stderr, "%v", errors.Wrap(err, "failed to close clients"))
		if err != nil {
			log.Println("copyStdin Fprintf:", err)
		}
	}
}

func (t *Task) clientsFinish(onPrefix bool, len int) {
	wg := &sync.WaitGroup{}

	for _, c := range t.Clients {
		wg.Add(1)
		go func(c Client) {
			var err error
			defer wg.Done()

			if err = c.Wait(); err == nil {
				return
			}

			prefix := ""
			if onPrefix {
				prefix = t.formatClientPrefix(c, len)
			}

			if e, ok := err.(*ssh.ExitError); ok && e.ExitStatus() != 15 {
				// TODO: Store all the errors, and print them after Wait().
				_, err = fmt.Fprintf(os.Stderr, "%s%v\n", prefix, e)
				if err != nil {
					log.Println("clientsFinish Fprintf:", err)
				}

				os.Exit(e.ExitStatus())
			}

			_, err = fmt.Fprintf(os.Stderr, "%s%v\n", prefix, err)
			if err != nil {
				log.Println("clientsFinish Fprintf:", err)
			}

			// TODO: Shouldn't os.Exit(1) here. Instead, collect the exit statuses for later.
			os.Exit(1)

		}(c)
	}

	wg.Wait()
}

type ErrTask struct {
	Task   *Task
	Reason string
}

func (e ErrTask) Error() string {
	return fmt.Sprintf(`Run("%v"): %v`, e.Task, e.Reason)
}
