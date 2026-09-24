//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/supervisor"
)

// A process can reach terminal recovery failure without consuming the retry
// budget. Exercise the real foreground command and supervisor, not a fabricated
// StateFailed snapshot. Moving the fence is fault injection, never recovery.
func TestMemoryServeReportsOwnershipFailureBeforeBudgetExhaustion(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("Go compiler unavailable: %v", err)
	}
	binDir, project := t.TempDir(), t.TempDir()
	source := filepath.Join(binDir, "daemon.go")
	const program = `package main
import("os";"os/signal";"syscall")
func main(){ c:=make(chan os.Signal,1);signal.Notify(c,os.Interrupt,syscall.SIGTERM);<-c }
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command(goTool, "build", "-o", filepath.Join(binDir, "cm"), source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, out)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	t.Setenv("PATH", binDir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runMemoryServe(ctx, io.Discard, port) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	session := fmt.Sprintf("memory-serve-%d", os.Getpid())
	pidPath := filepath.Join(project, ".ntm", "pids", "cm-"+session+".pid")
	var info supervisor.PIDFileInfo
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil && json.Unmarshal(data, &info) == nil && info.PID > 0 {
			break
		}
		select {
		case err := <-done:
			finished = true
			t.Fatalf("command exited before fault injection: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if info.PID <= 0 {
		t.Fatal("daemon PID not recorded")
	}
	locks, err := filepath.Glob(filepath.Join(project, ".ntm", "pids", ".daemon-*.lock"))
	if err != nil || len(locks) != 1 {
		t.Fatalf("ownership fences: %v, %v", locks, err)
	}
	if err := os.Rename(locks[0], locks[0]+".displaced"); err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(info.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Release()
	select {
	case err := <-done:
		finished = true
		if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), "recovery stopped") || !strings.Contains(err.Error(), "ownership") {
			t.Fatalf("terminal failure was hidden until cancellation: %v (context %v)", err, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("memory serve remained running after ownership recovery failed")
	}
}
