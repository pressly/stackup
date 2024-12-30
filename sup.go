package sup

import (
	"github.com/hashicorp/go-multierror"
	"github.com/mikkeloscar/sshconfig"
	"sync"

	"github.com/pkg/errors"
)

const VERSION = "0.5"

type Stackup struct {
	conf   *Supfile
	debug  bool
	prefix bool
}

func New(conf *Supfile) (*Stackup, error) {
	return &Stackup{
		conf: conf,
	}, nil
}

// Run runs set of commands on multiple hosts defined by network sequentially.
func (sup *Stackup) Run(sshConfigHosts []*sshconfig.SSHHost, network *Network, envVars EnvList, commands ...*Command) (err error) {
	if len(commands) == 0 {
		return errors.New("no commands to be run")
	}

	env := envVars.AsExport()

	// Create clients for every host (either SSH or Localhost).
	var bastion *SSHClient
	if network.Bastion != "" {
		if bastion, err = NewSSHClient(network.Bastion, "bastion", 0, sshConfigHosts); err != nil {
			return errors.Wrap(err, "create bastion")
		}

		if err = bastion.Connect(); err != nil {
			return errors.Wrap(err, "connecting to bastion failed")
		}
	}

	wg := &sync.WaitGroup{}
	clientCh := make(chan Client, len(network.Hosts))
	errCh := make(chan error, len(network.Hosts))

	i := 0
	wg.Add(len(network.Hosts))
	for _, host := range network.Hosts {
		i++
		go sup.networkHost(wg, clientCh, errCh, bastion, host, env, i, sshConfigHosts)
	}

	wg.Wait()
	close(clientCh)
	close(errCh)

	maxLen := 0
	var (
		clients          []Client
		deferRemoteClose []*SSHClient
	)
	deferRemoteClose = []*SSHClient{}

	for client := range clientCh {
		if remote, ok := client.(*SSHClient); ok {
			deferRemoteClose = append(deferRemoteClose, remote)
		}

		_, prefixLen := client.Prefix()
		if prefixLen > maxLen {
			maxLen = prefixLen
		}
		clients = append(clients, client)
	}

	defer func(deferRemoteClose []*SSHClient) {
		for _, r := range deferRemoteClose {
			if derr := r.Close(); derr != nil {
				err = multierror.Append(err, derr)
			}
		}
	}(deferRemoteClose)

	for err = range errCh {
		return errors.Wrap(err, "connecting to clients failed")
	}

	// Run command or run multiple commands defined by target sequentially.
	for _, cmd := range commands {
		var tasks []*Task
		// Translate command into task(s).
		if tasks, err = sup.createTasks(cmd, clients, env); err != nil {
			return errors.Wrap(err, "creating task failed")
		}

		// Run tasks sequentially.
		for _, task := range tasks {
			if err = task.do(sup.prefix, maxLen); err != nil {
				return
			}
		}
	}

	return
}

func (sup *Stackup) Debug(value bool) {
	sup.debug = value
}

func (sup *Stackup) Prefix(value bool) {
	sup.prefix = value
}

func (sup *Stackup) networkHost(wg *sync.WaitGroup, clientCh chan Client, errCh chan error,
	bastion *SSHClient, host string, env string, i int, sshConfigHosts []*sshconfig.SSHHost) {
	defer wg.Done()

	// Localhost client.
	if host == "localhost" {
		local := &LocalhostClient{
			env: env + `export SUP_HOST="` + host + `";`,
		}
		if err := local.Connect(); err != nil {
			errCh <- errors.Wrap(err, "connecting to localhost failed")
			return
		}
		clientCh <- local
		return
	}

	// SSH client.
	var (
		remote *SSHClient
		err    error
	)
	if remote, err = NewSSHClient(host, env, i, sshConfigHosts); err != nil {
		errCh <- errors.Wrap(err, "create new ssh client")
		return
	}

	if bastion != nil {
		if err = remote.ConnectWith(bastion.DialThrough); err != nil {
			errCh <- errors.Wrap(err, "connecting to remote host through bastion failed")
			return
		}

	} else {
		if err = remote.Connect(); err != nil {
			errCh <- errors.Wrap(err, "connecting to remote host failed")
			return
		}
	}

	clientCh <- remote
}
