package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/NovikovRoman/sup"
	"github.com/mikkeloscar/sshconfig"
	"github.com/pkg/errors"
)

var (
	supfile     string
	envVars     flagStringSlice
	sshConfig   string
	onlyHosts   string
	exceptHosts string

	debug         bool
	disablePrefix bool

	showVersion bool
	showHelp    bool

	ErrUsage          = errors.New("Usage: sup [OPTIONS] NETWORK COMMAND [...]\n       sup [ --help | -v | --version ]")
	ErrUnknownNetwork = errors.New("Unknown network")
	ErrNetworkNoHosts = errors.New("No hosts defined for a given network")
	ErrCmd            = errors.New("Unknown command/target")
)

type flagStringSlice []string

func (f *flagStringSlice) String() string {
	return fmt.Sprintf("%v", *f)
}

func (f *flagStringSlice) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func init() {
	flag.StringVar(&supfile, "f", "", "Custom path to ./Supfile[.yml]")
	flag.Var(&envVars, "e", "Set environment variables")
	flag.Var(&envVars, "env", "Set environment variables")
	flag.StringVar(&sshConfig, "sshconfig", "", "Read SSH Config file, ie. ~/.ssh/config file")
	flag.StringVar(&onlyHosts, "only", "", "Filter hosts using regexp")
	flag.StringVar(&exceptHosts, "except", "", "Filter out hosts using regexp")

	flag.BoolVar(&debug, "D", false, "Enable debug mode")
	flag.BoolVar(&debug, "debug", false, "Enable debug mode")
	flag.BoolVar(&disablePrefix, "disable-prefix", false, "Disable hostname prefix")

	flag.BoolVar(&showVersion, "v", false, "Print version")
	flag.BoolVar(&showVersion, "version", false, "Print version")
	flag.BoolVar(&showHelp, "h", false, "Show help")
	flag.BoolVar(&showHelp, "help", false, "Show help")
}

func networkUsage(conf *sup.Supfile) {
	w := &tabwriter.Writer{}
	w.Init(os.Stderr, 4, 4, 2, ' ', 0)
	defer func(w *tabwriter.Writer) {
		_ = w.Flush()
	}(w)

	// Print available networks/hosts.
	_, _ = fmt.Fprintln(w, "Networks:\t")
	for _, name := range conf.Networks.Names {
		_, _ = fmt.Fprintf(w, "- %v\n", name)
		network, _ := conf.Networks.Get(name)
		for _, host := range network.Hosts {
			_, _ = fmt.Fprintf(w, "\t- %v\n", host)
		}
	}
	_, _ = fmt.Fprintln(w)
}

func cmdUsage(conf *sup.Supfile) {
	w := &tabwriter.Writer{}
	w.Init(os.Stderr, 4, 4, 2, ' ', 0)
	defer func(w *tabwriter.Writer) {
		_ = w.Flush()
	}(w)

	// Print available targets/commands.
	_, _ = fmt.Fprintln(w, "Targets:\t")
	for _, name := range conf.Targets.Names {
		cmds, _ := conf.Targets.Get(name)
		_, _ = fmt.Fprintf(w, "- %v\t%v\n", name, strings.Join(cmds, " "))
	}
	_, _ = fmt.Fprintln(w, "\t")
	_, _ = fmt.Fprintln(w, "Commands:\t")
	for _, name := range conf.Commands.Names {
		cmd, _ := conf.Commands.Get(name)
		_, _ = fmt.Fprintf(w, "- %v\t%v\n", name, cmd.Desc)
	}
	_, _ = fmt.Fprintln(w)
}

// parseArgs parses args and returns network and commands to be run.
// On error, it prints usage and exits.
func parseArgs(conf *sup.Supfile) (network *sup.Network, commands []*sup.Command, err error) {
	var (
		ok bool
		nw sup.Network
	)

	args := flag.Args()
	if len(args) < 1 {
		networkUsage(conf)
		return nil, nil, ErrUsage
	}

	// Does the <network> exist?
	nw, ok = conf.Networks.Get(args[0])
	if !ok {
		networkUsage(conf)
		err = ErrUnknownNetwork
		return
	}

	network = &nw

	// Parse CLI --env flag env vars, override values defined in Network env.
	for _, env := range envVars {
		if len(env) == 0 {
			continue
		}
		i := strings.Index(env, "=")
		if i < 0 {
			if len(env) > 0 {
				network.Env.Set(env, "")
			}
			continue
		}
		network.Env.Set(env[:i], env[i+1:])
	}

	// Inventory
	var hosts []string
	if hosts, err = network.ParseInventory(); err != nil {
		return
	}
	network.Hosts = append(network.Hosts, hosts...)

	// Does the <network> have at least one host?
	if len(network.Hosts) == 0 {
		networkUsage(conf)
		err = ErrNetworkNoHosts
		return
	}

	// Check for the second argument
	if len(args) < 2 {
		cmdUsage(conf)
		err = ErrUsage
		return
	}

	// In case of the network.Env needs an initialization
	if network.Env == nil {
		network.Env = make(sup.EnvList, 0)
	}

	// Add default env variable with current network
	network.Env.Set("SUP_NETWORK", args[0])

	// Add default nonce
	network.Env.Set("SUP_TIME", time.Now().UTC().Format(time.RFC3339))
	if os.Getenv("SUP_TIME") != "" {
		network.Env.Set("SUP_TIME", os.Getenv("SUP_TIME"))
	}

	// Add user
	if os.Getenv("SUP_USER") != "" {
		network.Env.Set("SUP_USER", os.Getenv("SUP_USER"))
	} else {
		network.Env.Set("SUP_USER", os.Getenv("USER"))
	}

	for _, cmd := range args[1:] {
		// Target?
		target, isTarget := conf.Targets.Get(cmd)
		if isTarget {
			// Loop over target's commands.
			for _, cmdTarget := range target {
				command, isCommand := conf.Commands.Get(cmdTarget)
				if !isCommand {
					cmdUsage(conf)
					err = fmt.Errorf("%v: %v", ErrCmd, cmdTarget)
					return
				}
				command.Name = cmdTarget
				commands = append(commands, &command)
			}
		}

		// Command?
		command, isCommand := conf.Commands.Get(cmd)
		if isCommand {
			command.Name = cmd
			commands = append(commands, &command)
		}

		if !isTarget && !isCommand {
			cmdUsage(conf)
			return nil, nil, fmt.Errorf("%v: %v", ErrCmd, cmd)
		}
	}

	return
}

func resolvePath(path string) string {
	if path == "" {
		return ""
	}
	if path[:2] == "~/" {
		usr, err := user.Current()
		if err == nil {
			path = filepath.Join(usr.HomeDir, path[2:])
		}
	}
	return path
}

func main() {
	var (
		conf     *sup.Supfile
		commands []*sup.Command
		network  *sup.Network
		data     []byte
		err      error
	)
	flag.Parse()

	if showHelp {
		_, _ = fmt.Fprintln(os.Stderr, ErrUsage, "\n\nOptions:")
		flag.PrintDefaults()
		return
	}

	if showVersion {
		_, _ = fmt.Fprintln(os.Stderr, sup.VERSION)
		return
	}

	if supfile == "" {
		supfile = "./Supfile"
	}

	if data, err = ioutil.ReadFile(resolvePath(supfile)); err != nil {
		firstErr := err
		data, err = ioutil.ReadFile("./Supfile.yml") // Alternative to ./Supfile.
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, firstErr)
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	if conf, err = sup.NewSupfile(data); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Parse network and commands to be run from args.
	if network, commands, err = parseArgs(conf); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var (
		expr *regexp.Regexp
	)

	// --only flag filters hosts
	if onlyHosts != "" {
		if expr, err = regexp.CompilePOSIX(onlyHosts); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		var hosts []string
		for _, host := range network.Hosts {
			if expr.MatchString(host) {
				hosts = append(hosts, host)
			}
		}
		if len(hosts) == 0 {
			_, _ = fmt.Fprintln(os.Stderr, fmt.Errorf("no hosts match --only '%v' regexp", onlyHosts))
			os.Exit(1)
		}
		network.Hosts = hosts
	}

	// --except flag filters out hosts
	if exceptHosts != "" {
		if expr, err = regexp.CompilePOSIX(exceptHosts); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		var hosts []string
		for _, host := range network.Hosts {
			if !expr.MatchString(host) {
				hosts = append(hosts, host)
			}
		}

		if len(hosts) == 0 {
			_, _ = fmt.Fprintln(os.Stderr, fmt.Errorf("no hosts left after --except '%v' regexp", onlyHosts))
			os.Exit(1)
		}
		network.Hosts = hosts
	}

	// --sshconfig flag location for ssh_config file
	if sshConfig == "" {
		sshConfig = filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	}

	var sshConfigHosts []*sshconfig.SSHHost
	if sshConfigHosts, err = sshconfig.Parse(resolvePath(sshConfig)); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var vars sup.EnvList
	for _, val := range append(conf.Env, network.Env...) {
		vars.Set(val.Key, val.Value)
	}
	if err = vars.ResolveValues(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Parse CLI --env flag env vars, define $SUP_ENV and override values defined in Supfile.
	var cliVars sup.EnvList
	for _, env := range envVars {
		if len(env) == 0 {
			continue
		}
		i := strings.Index(env, "=")
		if i < 0 {
			if len(env) > 0 {
				vars.Set(env, "")
			}
			continue
		}
		vars.Set(env[:i], env[i+1:])
		cliVars.Set(env[:i], env[i+1:])
	}

	// SUP_ENV is generated only from CLI env vars.
	// Separate loop to omit duplicates.
	supEnv := ""
	for _, v := range cliVars {
		supEnv += fmt.Sprintf(" -e %v=%q", v.Key, v.Value)
	}
	vars.Set("SUP_ENV", strings.TrimSpace(supEnv))

	// Create new Stackup app.
	app, err := sup.New(conf)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app.Debug(debug)
	app.Prefix(!disablePrefix)

	// Run all the commands in the given network.
	err = app.Run(sshConfigHosts, network, vars, commands...)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
