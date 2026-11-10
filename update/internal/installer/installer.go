// Package installer runs update tools that report like "rauc install":
// progress lines "NN% message" and, on failure, "LastError: message".
package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Spool writes a pushed image to a new file in dir, which is created if
// needed, and returns its path.
func Spool(dir, pattern string, r io.Reader) (path string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		return "", fmt.Errorf("receiving bundle: %w", err)
	}
	return f.Name(), nil
}

// Run runs command with args, reporting the percentages of its progress
// lines. On failure the error starts with the command's name and its first
// argument and carries its LastError, or else its last line of output.
func Run(ctx context.Context, command string, args []string, progress func(int)) error {
	cmd := exec.CommandContext(ctx, command, args...)
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return err
	}

	var lastErr, lastLine string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if pct, ok := parseProgress(line); ok {
				progress(pct)
			} else if msg, ok := strings.CutPrefix(line, "LastError: "); ok {
				lastErr = msg
			} else if line != "" {
				lastLine = line
			}
		}
		_, _ = io.Copy(io.Discard, r) // unblock the command after an overlong line
	}()
	err := cmd.Wait()
	w.Close()
	<-done

	if err == nil {
		return nil
	}
	msg := lastErr
	if msg == "" {
		msg = lastLine
	}
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(filepath.Base(command) + " " + args[0] + ": " + msg)
}

// parseProgress parses a progress line such as "65% Copying image to rootfs".
func parseProgress(line string) (int, bool) {
	pct, _, ok := strings.Cut(line, "% ")
	if !ok {
		pct, ok = strings.CutSuffix(line, "%")
	}
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(pct)
	return n, err == nil
}
