package sup

import (
	"fmt"
	"github.com/hashicorp/go-multierror"
	"github.com/mikkeloscar/sshconfig"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
)

// SSHClient is a wrapper over the SSH connection/sessions.
type SSHClient struct {
	conn         *ssh.Client
	sess         *ssh.Session
	user         string
	host         string
	remoteStdin  io.WriteCloser
	remoteStdout io.Reader
	remoteStderr io.Reader
	connOpened   bool
	sessOpened   bool
	running      bool
	env          string //export FOO="bar"; export BAR="baz";
	color        string
	signer       *ssh.Signer
}

func NewSSHClient(host string, env string, i int, sshConfigHosts []*sshconfig.SSHHost) (c *SSHClient, err error) {
	c = &SSHClient{
		host:   host,
		color:  Colors[i%len(Colors)],
		signer: nil,
	}

	for _, sshHost := range sshConfigHosts {
		for _, h := range sshHost.Host {
			if host != h {
				continue
			}

			c.host = sshHost.HostName
			c.user = sshHost.User
			if sshHost.Port > 0 {
				c.host = fmt.Sprintf("%s:%d", c.host, sshHost.Port)
			}

			if sshHost.IdentityFile != "" {
				if strings.HasPrefix(sshHost.IdentityFile, "~") {
					sshHost.IdentityFile = strings.Replace(sshHost.IdentityFile, "~", os.Getenv("HOME"), 1)
				}

				if c.signer, err = c.getPrivateKey(sshHost.IdentityFile); err != nil {
					err = errors.Wrap(err, "get private key")
					return
				}
			}

			c.env = env + `export SUP_HOST="` + c.host + `";`
			return
		}
	}

	c.env = env + `export SUP_HOST="` + host + `";`
	err = c.parseHost(host)
	return
}

type ErrConnect struct {
	User   string
	Host   string
	Reason string
}

func (e ErrConnect) Error() string {
	return fmt.Sprintf(`Connect("%v@%v"): %v`, e.User, e.Host, e.Reason)
}

// SSHDialFunc can dial a ssh server and return a client
type SSHDialFunc func(net, addr string, config *ssh.ClientConfig) (*ssh.Client, error)

// Connect creates SSH connection to a specified host.
// It expects the host of the form "[ssh://]host[:port]".
func (c *SSHClient) Connect() error {
	return c.ConnectWith(ssh.Dial)
}

// ConnectWith creates a SSH connection to a specified host. It will use dialer to establish the
// connection.
// TODO: Split Signers to its own method.
func (c *SSHClient) ConnectWith(dialer SSHDialFunc) (err error) {
	if c.connOpened {
		return errors.New("Already connected")
	}

	initAuthMethodOnce.Do(initAuthMethod)

	var auth []ssh.AuthMethod
	if c.signer == nil {
		auth = []ssh.AuthMethod{ssh.PublicKeys(signers...)}

	} else {
		auth = []ssh.AuthMethod{
			ssh.PublicKeys(*c.signer),
		}
	}

	config := &ssh.ClientConfig{
		User:            c.user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	if c.conn, err = dialer("tcp", c.host, config); err != nil {
		return ErrConnect{c.user, c.host, err.Error()}
	}

	c.connOpened = true
	return
}

func (c *SSHClient) getPrivateKey(file string) (*ssh.Signer, error) {
	var (
		data   []byte
		signer ssh.Signer
		err    error
	)

	if strings.HasSuffix(file, ".pub") {
		return nil, err
	}

	if data, err = ioutil.ReadFile(file); err != nil {
		return nil, err
	}

	signer, err = ssh.ParsePrivateKey(data)
	return &signer, err
}

// Run runs the task.Run command remotely on c.host.
func (c *SSHClient) Run(task *Task) error {
	if c.running {
		return errors.New("Session already running")
	}
	if c.sessOpened {
		return errors.New("Session already connected")
	}

	sess, err := c.conn.NewSession()
	if err != nil {
		return err
	}

	c.remoteStdin, err = sess.StdinPipe()
	if err != nil {
		return err
	}

	c.remoteStdout, err = sess.StdoutPipe()
	if err != nil {
		return err
	}

	c.remoteStderr, err = sess.StderrPipe()
	if err != nil {
		return err
	}

	if task.TTY {
		// Set up terminal modes
		modes := ssh.TerminalModes{
			ssh.ECHO:          0,     // disable echoing
			ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4k baud
			ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4k baud
		}
		// Request pseudo terminal
		if err = sess.RequestPty("xterm", 80, 40, modes); err != nil {
			return ErrTask{task, fmt.Sprintf("request for pseudo terminal failed: %s", err)}
		}
	}

	// Start the remote command.
	if err = sess.Start(c.env + task.Run); err != nil {
		return ErrTask{task, err.Error()}
	}

	c.sess = sess
	c.sessOpened = true
	c.running = true
	return nil
}

// Wait waits until the remote command finishes and exits.
// It closes the SSH session.
func (c *SSHClient) Wait() (err error) {
	if !c.running {
		return errors.New("Trying to wait on stopped session")
	}

	err = c.sess.Wait()
	c.running = false
	c.sessOpened = false

	if e := c.sess.Close(); e != nil && e != io.EOF {
		err = multierror.Append(err, e)
	}
	return
}

// DialThrough will create a new connection from the ssh server sc is connected to. DialThrough is an SSHDialer.
func (c *SSHClient) DialThrough(n, addr string, config *ssh.ClientConfig) (sc *ssh.Client, err error) {
	var (
		cc    ssh.Conn
		nChan <-chan ssh.NewChannel
		reqs  <-chan *ssh.Request
		conn  net.Conn
	)

	if conn, err = c.conn.Dial(n, addr); err != nil {
		return
	}

	if cc, nChan, reqs, err = ssh.NewClientConn(conn, addr, config); err != nil {
		return
	}

	sc = ssh.NewClient(cc, nChan, reqs)
	return

}

// Close closes the underlying SSH connection and session.
func (c *SSHClient) Close() (err error) {
	if c.sessOpened {
		c.sessOpened = false
		if err = c.sess.Close(); err != nil {
			return
		}
	}

	if !c.connOpened {
		return errors.New("Trying to close the already closed connection")
	}

	err = c.conn.Close()
	c.connOpened = false
	c.running = false
	return err
}

func (c *SSHClient) Stdin() io.WriteCloser {
	return c.remoteStdin
}

func (c *SSHClient) Stderr() io.Reader {
	return c.remoteStderr
}

func (c *SSHClient) Stdout() io.Reader {
	return c.remoteStdout
}

func (c *SSHClient) Prefix() (string, int) {
	host := c.user + "@" + c.host + " | "
	return c.color + host + ResetColor, len(host)
}

func (c *SSHClient) Write(p []byte) (n int, err error) {
	return c.remoteStdin.Write(p)
}

func (c *SSHClient) WriteClose() error {
	return c.remoteStdin.Close()
}

func (c *SSHClient) Signal(sig os.Signal) error {
	if !c.sessOpened {
		return errors.New("session is not open")
	}

	switch sig {
	case os.Interrupt:
		// TODO: Turns out that .Signal(ssh.SIGHUP) doesn't work for me.
		// Instead, sending \x03 to the remote session works for me,
		// which sounds like something that should be fixed/resolved
		// upstream in the golang.org/x/crypto/ssh pkg.
		// https://github.com/golang/go/issues/4115#issuecomment-66070418
		_, _ = c.remoteStdin.Write([]byte("\x03"))
		return c.sess.Signal(ssh.SIGINT)

	default:
		return fmt.Errorf("%v not supported", sig)
	}
}

func (c *SSHClient) parseHost(host string) (err error) {
	var (
		u *user.User
	)

	// Remove extra "ssh://" schema
	if len(host) > 6 && host[:6] == "ssh://" {
		host = host[6:]
	}

	// Split by the last "@", since there may be an "@" in the username.
	if at := strings.LastIndex(host, "@"); at != -1 {
		c.user = host[:at]
		host = host[at+1:]
	}

	// Add default user, if not set
	if c.user == "" {
		if u, err = user.Current(); err != nil {
			return
		}
		c.user = u.Username
	}

	if strings.Index(host, "/") != -1 {
		err = ErrConnect{User: c.user, Host: host, Reason: "unexpected slash in the host URL"}
		return
	}

	// Add default port, if not set
	if at := strings.LastIndex(host, ":"); at != -1 {
		c.host += ":22"
	}

	return
}

var (
	initAuthMethodOnce sync.Once
	signers            []ssh.Signer
)

// initAuthMethod initiates SSH authentication method.
func initAuthMethod() {
	var (
		data   []byte
		signer ssh.Signer
	)

	// If there's a running SSH Agent, try to use its Private keys.
	sock, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err == nil {
		agentClient := agent.NewClient(sock)
		signers, _ = agentClient.Signers()
	}

	// Try to read user's SSH private keys form the standard paths.
	files, _ := filepath.Glob(os.Getenv("HOME") + "/.ssh/id_*")
	for _, file := range files {
		if strings.HasSuffix(file, ".pub") {
			continue // Skip public keys.
		}
		data, err = ioutil.ReadFile(file)
		if err != nil {
			continue
		}
		signer, err = ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
}
