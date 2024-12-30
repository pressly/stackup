//go:build darwin
// +build darwin

package sup

import (
	"syscall"
)

func getProcAttrs(tty bool) *syscall.SysProcAttr {
	attrs := &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(syscall.Getuid()),
			Gid: uint32(syscall.Getgid()),
		},
	}

	if tty {
		attrs.Setpgid = true
		attrs.Setsid = true
	}

	return attrs
}
