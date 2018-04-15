package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

// setupMockEnv prepares testing environment, it
//
// - creates a temporary directory for all files
// - generates RSA key pair; the private key is written into a file,
//   fingerprint of the public key is written into a file as an authorized key
// - spins up mock SSH servers with the same authorized key
// - writes an SSH config file with entries for all servers, naming them
//   server0, server1 etc.
func setupMockEnv(dirname string, count int) ([]bytes.Buffer, options, error) {

	privateKeyPath := path.Join(dirname, "gotest_private_key")
	authorizedKeysPath := path.Join(dirname, "authorized_keys")
	sshConfigPath := path.Join(dirname, "ssh_config")

	if err := generateKeyPair(privateKeyPath, authorizedKeysPath); err != nil {
		return nil, options{}, err
	}

	outputs := make([]bytes.Buffer, count)
	addresses := make([]string, count)
	for i := 0; i < count; i++ {
		runTestServer(authorizedKeysPath, &addresses[i], &outputs[i])
	}

	err := writeSSHConfigFile(privateKeyPath, sshConfigPath, addresses)
	if err != nil {
		return nil, options{}, err
	}

	options := options{
		sshConfig: sshConfigPath,
		dirname:   dirname,
		env:       testEnv(),
	}
	return outputs, options, nil
}

// generateKeyPair generates a pair of keys, the private key is written into
// a file and the fingerprint of the public key into authorized_keys file for
// the server to use
func generateKeyPair(privateKeyPath, authorizedKeysPath string) error {
	privateKey, err := generatePrivateRSAKey()
	if err != nil {
		return err
	}
	if err := writePrivateKeyToFile(privateKey, privateKeyPath); err != nil {
		return err
	}

	publicKey := privateKey.PublicKey
	pub, err := ssh.NewPublicKey(&publicKey)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(
		authorizedKeysPath,
		ssh.MarshalAuthorizedKey(pub),
		0666,
	)
}

func generatePrivateRSAKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2014)
}

func writePrivateKeyToFile(privateKey *rsa.PrivateKey, filepath string) error {
	privateKeyBlock := pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(privateKey),
	}
	return ioutil.WriteFile(
		filepath,
		pem.EncodeToMemory(&privateKeyBlock),
		0666,
	)
}

func runTestServer(authorizedKeysPath string, addr *string, out io.Writer) error {
	authorizedKeysMap, err := loadAuthorizedKeys(authorizedKeysPath)
	if err != nil {
		return err
	}

	config, err := buildServerConfig(authorizedKeysMap)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", "localhost:")
	if err != nil {
		return errors.Wrap(err, "failed to listen for connection")
	}
	*addr = listener.Addr().String()

	go sshListen(config, listener, out)

	return nil
}

func buildServerConfig(authorizedKeysMap map[string]bool) (*ssh.ServerConfig, error) {
	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &ssh.ServerConfig{
		// Remove to disable public key auth.
		PublicKeyCallback: func(c ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
			if authorizedKeysMap[string(pubKey.Marshal())] {
				return &ssh.Permissions{
					// Record the public key used for authentication.
					Extensions: map[string]string{
						"pubkey-fp": fingerprintSHA256(pubKey),
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown public key for %q", c.User())
		},
	}

	key, err := generatePrivateRSAKey()
	if err != nil {
		return nil, err
	}

	private, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}

	config.AddHostKey(private)
	return config, nil
}

func sshListen(config *ssh.ServerConfig, listener net.Listener, out io.Writer) {
	func() {
		nConn, err := listener.Accept()
		if err != nil {
			panic(errors.Wrap(err, "failed to accept incoming connection"))
		}

		// Before use, a handshake must be performed on the incoming
		// net.Conn.
		_, chans, reqs, err := ssh.NewServerConn(nConn, config)
		if err != nil {
			panic(errors.Wrap(err, "failed to handshake"))
		}

		// The incoming Request channel must be serviced.
		go ssh.DiscardRequests(reqs)

		// Service the incoming Channel channel.
		for newChannel := range chans {
			// Channels have a type, depending on the application level
			// protocol intended. In the case of a shell, the type is
			// "session" and ServerShell may be used to present a simple
			// terminal interface.
			if newChannel.ChannelType() != "session" {
				newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				panic(errors.Wrap(err, "Could not accept channel"))
			}

			go func(in <-chan *ssh.Request) {
				defer channel.Close()

				for req := range in {
					// reply to pty-req with success
					if req.Type == "pty-req" {
						req.Reply(true, []byte{})

						// read exec command, write it to output and respond with success
					} else if req.Type == "exec" {
						type execMsg struct {
							Command string
						}
						var payload execMsg
						ssh.Unmarshal(req.Payload, &payload)
						out.Write([]byte(payload.Command + "\n"))
						req.Reply(true, nil)

						channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
						if err := channel.Close(); err != nil {
							panic(err)
						}
					}
				}
			}(requests)
		}
	}()
}

func fingerprintSHA256(pubKey ssh.PublicKey) string {
	sha256sum := sha256.Sum256(pubKey.Marshal())
	hash := base64.RawStdEncoding.EncodeToString(sha256sum[:])
	return "SHA256:" + hash
}

func loadAuthorizedKeys(filepath string) (map[string]bool, error) {
	authorizedKeysBytes, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to load %sv", filepath)
	}
	authorizedKeysMap := map[string]bool{}
	for len(authorizedKeysBytes) > 0 {
		pubKey, _, _, rest, err := ssh.ParseAuthorizedKey(authorizedKeysBytes)
		if err != nil {
			return nil, err
		}

		authorizedKeysMap[string(pubKey.Marshal())] = true
		authorizedKeysBytes = rest
	}
	return authorizedKeysMap, nil
}

// writes simple SSH config file for the given servers naming them server0,
// server1 etc.
func writeSSHConfigFile(privateKeyPath, sshConfigPath string, addresses []string) error {
	type sshRecord struct {
		Host             string
		Port             string
		IdentityFilename string
	}
	records := make([]sshRecord, len(addresses))
	for i, addr := range addresses {
		records[i].Host = fmt.Sprintf("server%d", i)
		records[i].IdentityFilename = privateKeyPath
		records[i].Port = strings.Split(addr, ":")[1]
	}

	sshConfigTemplate := `
{{range .Records}}
Host {{.Host}}
  HostName localhost
  Port {{.Port}}
  IdentityFile {{.IdentityFilename}}
{{end}}
`

	tmpl := template.New("ssh_config")
	tmpl, err := tmpl.Parse(sshConfigTemplate)
	if err != nil {
		return err
	}

	file, err := os.Create(sshConfigPath)
	if err != nil {
		return err
	}
	defer file.Close()

	data := struct {
		Records []sshRecord
	}{
		Records: records,
	}

	if err := tmpl.Execute(file, data); err != nil {
		return err
	}

	return nil
}
