package standalone_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/standalone"
)

func TestDataDirLockAcrossProcesses(t *testing.T) {
	if directory := os.Getenv("AGENTMESH_TEST_LOCK_CHILD"); directory != "" {
		app := newApp(t, directory)
		defer app.Close(context.Background())
		fmt.Fprintln(os.Stdout, "lock-acquired")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDataDirLockAcrossProcesses$")
	child.Env = append(os.Environ(), "AGENTMESH_TEST_LOCK_CHILD="+directory)
	child.Stderr = os.Stderr
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	if output, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(output) != "lock-acquired" {
		t.Fatalf("lock helper: %q, %v", output, err)
	}
	if _, err := standalone.New(t.Context(), standalone.Config{DataDir: directory}); err == nil || err.Error() != "standalone_data_dir_locked" {
		t.Fatalf("second standalone lock error = %v", err)
	}
	source := filepath.Join(t.TempDir(), "backup")
	if err := standalone.RestoreDataDir(source, directory); err == nil || err.Error() != "standalone_data_dir_locked" {
		t.Fatalf("restore lock error = %v", err)
	}
	if err := standalone.RestoreDataDir(directory, t.TempDir()); err == nil || err.Error() != "standalone_data_dir_locked" {
		t.Fatalf("restore source lock error = %v", err)
	}
	app := newApp(t, t.TempDir())
	defer app.Close(context.Background())
	if err := app.Backup(t.Context(), directory); err == nil || err.Error() != "standalone_data_dir_locked" {
		t.Fatalf("backup destination lock error = %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	reopened := newApp(t, directory)
	defer reopened.Close(context.Background())
}
