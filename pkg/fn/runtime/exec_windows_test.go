// Copyright 2026 The kpt Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build windows

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fnresultv1 "github.com/kptdev/kpt/api/fnresult/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// helperEnv selects the mode of TestExecFnHelperProcess.
const helperEnv = "KPT_EXEC_TEST_HELPER"

// TestExecFnHelperProcess is not a real test. The tests run the test binary
// with it as the function, and the function runs it again as its child. The
// mode is given in helperEnv and the pid file as the argument after "--".
// The function writes the pid of its child and its own pid to the pid file.
func TestExecFnHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	if mode == "child" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}

	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "no pid file given")
		os.Exit(2)
	}
	pidFile := args[1]

	// the environment of the function has only helperEnv, so the child gets
	// it in the same way
	child := exec.Command(os.Args[0], "-test.run=^TestExecFnHelperProcess$")
	child.Env = []string{helperEnv + "=child"}
	if mode == "leave-child" {
		// the child has the output of the function open
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
	}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := writePidFile(pidFile, child.Process.Pid, os.Getpid()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	switch mode {
	case "wait-for-child":
		_ = child.Wait()
		os.Exit(0)
	case "leave-child":
		// exit when the test creates the exit gate
		for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
			if _, err := os.Stat(exitGate(pidFile)); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(0)
	case "leave-detached-child":
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(2)
	}
}

// exitGate is the file that the test creates to let the function exit.
func exitGate(pidFile string) string {
	return pidFile + ".exit"
}

// writePidFile writes the pids to path at once, so that a reader never sees
// a part of them.
func writePidFile(path string, childPid, fnPid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, fmt.Appendf(nil, "%d %d\n", childPid, fnPid), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readPidFile returns the pids in path, or zeros if they are not there yet.
func readPidFile(path string) (childPid, fnPid uint32) {
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(data), "\n") {
		return 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0
	}
	child, err1 := strconv.ParseUint(fields[0], 10, 32)
	fn, err2 := strconv.ParseUint(fields[1], 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return uint32(child), uint32(fn)
}

// helperArgs are the arguments that run the helper with pidFile.
func helperArgs(pidFile string) []string {
	return []string{"-test.run=^TestExecFnHelperProcess$", "--", pidFile}
}

// waitForPids waits for the pid file of the helper and returns its pids.
func waitForPids(t *testing.T, pidFile string) (childPid, fnPid uint32) {
	t.Helper()
	require.Eventually(t, func() bool {
		childPid, fnPid = readPidFile(pidFile)
		return childPid != 0
	}, time.Minute, 10*time.Millisecond, "the function did not start its child")
	return childPid, fnPid
}

// openProcess returns a handle of the process pid, which is terminated and
// closed when the test finishes.
func openProcess(t *testing.T, pid uint32) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, pid)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = windows.TerminateProcess(h, 1)
		_, _ = windows.WaitForSingleObject(h, 10000)
		_ = windows.CloseHandle(h)
	})
	return h
}

// assertWait checks that waiting for h for at most ms milliseconds ends
// with want.
func assertWait(t *testing.T, h windows.Handle, ms uint32, want uint32, msg string) {
	t.Helper()
	event, err := windows.WaitForSingleObject(h, ms)
	require.NoError(t, err)
	assert.Equal(t, want, event, msg)
}

// waitResult returns the result from done, and fails the test if it does not
// come in time.
func waitResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Minute):
		t.Fatal("the function did not finish")
		return nil
	}
}

// newHelperCmd returns the helper in mode as the function, run with ctx.
func newHelperCmd(ctx context.Context, mode, pidFile string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], helperArgs(pidFile)...)
	cmd.Env = []string{helperEnv + "=" + mode}
	// buffers make os/exec create pipes, as for ExecFn
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	return cmd
}

// assertExitCode1 checks that err is the exit of a process that was killed.
func assertExitCode1(t *testing.T, err error) {
	t.Helper()
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	require.True(t, ok, "unexpected error: %v", err)
	assert.Equal(t, 1, exitErr.ExitCode())
}

func TestExecRunCmd_ContextEndStopsChildProcesses(t *testing.T) {
	testCases := map[string]struct {
		mode string
		// the function exits before ctx ends
		exitsFirst bool
	}{
		"function that waits for its child": {
			mode: "wait-for-child",
		},
		"function that has exited and left a child with its output open": {
			mode:       "leave-child",
			exitsFirst: true,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			cmd := newHelperCmd(ctx, tc.mode, pidFile)

			done := make(chan error, 1)
			go func() {
				done <- runCmd(ctx, cmd)
			}()

			childPid, fnPid := waitForPids(t, pidFile)
			child := openProcess(t, childPid)
			assertWait(t, child, 0, uint32(windows.WAIT_TIMEOUT), "the child is not running")
			if tc.exitsFirst {
				fn := openProcess(t, fnPid)
				require.NoError(t, os.WriteFile(exitGate(pidFile), nil, 0o600))
				assertWait(t, fn, 30000, uint32(windows.WAIT_OBJECT_0), "the function did not exit")
				assertWait(t, child, 0, uint32(windows.WAIT_TIMEOUT), "the child is not running")
			}

			cancel()
			err := waitResult(t, done)

			if !tc.exitsFirst {
				assertExitCode1(t, err)
			}
			assertWait(t, child, 10000, uint32(windows.WAIT_OBJECT_0), "the child is still running")
		})
	}
}

func TestExecRunCmd_ContextEndWhileSuspended(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// the function never runs, so it needs no pid file
	cmd := newHelperCmd(ctx, "child", filepath.Join(t.TempDir(), "pid"))

	// end ctx while the function is suspended, and let runCmd go on when the
	// function has been killed
	killed := uint32(windows.WAIT_FAILED)
	origBeforeResume := beforeResume
	beforeResume = func() {
		cancel()
		_ = cmd.Process.WithHandle(func(h uintptr) {
			killed, _ = windows.WaitForSingleObject(windows.Handle(h), 10000)
		})
	}
	t.Cleanup(func() { beforeResume = origBeforeResume })

	done := make(chan error, 1)
	go func() {
		done <- runCmd(ctx, cmd)
	}()
	err := waitResult(t, done)

	assert.Equal(t, uint32(windows.WAIT_OBJECT_0), killed, "the function was not killed")
	assertExitCode1(t, err)
}

func TestExecFn_NormalFinishLeavesDetachedChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	fn := &ExecFn{
		Path:     os.Args[0],
		Args:     helperArgs(pidFile),
		Env:      map[string]string{helperEnv: "leave-detached-child"},
		Timeout:  time.Minute,
		FnResult: &fnresultv1.Result{},
	}

	done := make(chan error, 1)
	go func() {
		done <- fn.Run(&bytes.Buffer{}, &bytes.Buffer{})
	}()
	require.NoError(t, waitResult(t, done))

	childPid, _ := waitForPids(t, pidFile)
	child := openProcess(t, childPid)
	assertWait(t, child, 500, uint32(windows.WAIT_TIMEOUT), "the child was stopped")
}
