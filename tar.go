package sup

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

// Copying dirs/files over SSH using TAR.
// tar -C . -cvzf - $SRC | ssh $HOST "tar -C $DST -xvzf -"

// RemoteTarCommand returns command to be run on remote SSH host
// to properly receive the created TAR stream.
// TODO: Check for relative directory.
func RemoteTarCommand(dir string) string {
	return fmt.Sprintf("tar -C \"%s\" -xzf -", dir)
}

func LocalTarCmdArgs(path, exclude string) []string {
	var args []string

	// Added pattens to exclude from tar compress
	excludes := strings.Split(exclude, ",")
	for _, exc := range excludes {
		trimmed := strings.TrimSpace(exc)
		if trimmed != "" {
			args = append(args, `--exclude=`+trimmed)
		}
	}

	args = append(args, "-C", ".", "-czf", "-", path)
	return args
}

// NewTarStreamReader creates a tar stream reader from a local path.
// TODO: Refactor. Use "archive/tar" instead.
func NewTarStreamReader(cwd, path, exclude string) (stdout io.Reader, err error) {
	cmd := exec.Command("tar", LocalTarCmdArgs(path, exclude)...)
	cmd.Dir = cwd

	if stdout, err = cmd.StdoutPipe(); err != nil {
		err = errors.Wrap(err, "tar: stdout pipe failed")

	} else if err = cmd.Start(); err != nil {
		err = errors.Wrap(err, "tar: starting cmd failed")
	}

	return
}
