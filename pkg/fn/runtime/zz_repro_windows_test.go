//go:build windows

package runtime

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestZZWaitWithoutSynchronize(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 4 127.0.0.1 > NUL")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	started := time.Now()
	event, waitErr := windows.WaitForSingleObject(process, windows.INFINITE)
	t.Logf("RESULT wait returned event=%#x err=%v after %v (process runs about 3s)", event, waitErr, time.Since(started))
}

func TestZZNormalFunctionCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "ping -n 4 127.0.0.1 > NUL & echo done")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	err := runCmd(ctx, cmd)
	t.Logf("RESULT normal function err=%v elapsed=%v stdout=%q stderr=%q", err, time.Since(started), strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
	if err != nil || !strings.Contains(stdout.String(), "done") {
		t.Errorf("a function with no timeout should finish and write its output")
	}
}

func TestZZTimeoutStopsDescendant(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c",
		`start /b cmd /c "ping -n 8 127.0.0.1 > NUL & echo x > `+marker+`" & ping -n 30 127.0.0.1 > NUL`)
	started := time.Now()
	err := runCmd(ctx, cmd)
	t.Logf("RESULT timeout err=%v elapsed=%v", err, time.Since(started))
	time.Sleep(10 * time.Second)
	_, statErr := os.Stat(marker)
	t.Logf("RESULT descendant wrote marker=%v", statErr == nil)
	if statErr == nil {
		t.Errorf("the descendant should have been stopped")
	}
}

func TestZZChildHoldsStdoutAfterExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "start /b ping -n 40 127.0.0.1 & ping -n 3 127.0.0.1 > NUL & echo parent-done")
	cmd.Stdout = &stdout
	started := time.Now()
	err := runCmd(ctx, cmd)
	elapsed := time.Since(started)
	t.Logf("RESULT held stdout err=%v elapsed=%v parentDone=%v", err, elapsed, strings.Contains(stdout.String(), "parent-done"))
	if elapsed > 20*time.Second {
		t.Errorf("runCmd waited for the child that held stdout")
	}
}
