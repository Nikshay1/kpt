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
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procGetProcessIdOfThread is not in golang.org/x/sys/windows.
var procGetProcessIdOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

// beforeResume runs right before the function is resumed. It is a variable so
// that the tests can replace it.
var beforeResume = func() {}

// runCmd starts cmd in a job object of its own and waits for it to finish.
// The processes that the function starts are in the same job, so when ctx
// ends, the whole job is terminated, also after the function has exited.
func runCmd(ctx context.Context, cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		// without a job only the function is killed when ctx ends
		return cmd.Run()
	}
	defer func() { _ = windows.CloseHandle(job) }()

	// start the function suspended, so that it cannot start a process before
	// it is in the job
	attr := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		*attr = *cmd.SysProcAttr
	}
	attr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.SysProcAttr = attr

	if err := cmd.Start(); err != nil {
		return err
	}

	var assignErr error
	if err := cmd.Process.WithHandle(func(h uintptr) {
		assignErr = windows.AssignProcessToJobObject(job, windows.Handle(h))
	}); err != nil {
		assignErr = err
	}

	if assignErr == nil {
		// When ctx ends, cmd kills the function, and only if it is still
		// running. A process that the function started can outlive the
		// function and keep its output open, so terminate the job, also after
		// the function has exited. Register this before the function runs, so
		// that a deadline that passes meanwhile is not missed.
		terminated := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(terminated)
			// exit code 1, as the Kill of a process
			_ = windows.TerminateJobObject(job, 1)
		})
		// wait for the job to be terminated before its handle is closed
		defer func() {
			if !stop() {
				<-terminated
			}
		}()
	}
	// If the function cannot be put in the job, e.g. where nested jobs are
	// not allowed, run it without the job, as before, rather than fail.

	beforeResume()
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		// the function was killed because ctx ended before it could run
		if ctx.Err() != nil {
			return waitErr
		}
		return fmt.Errorf("resuming the function: %w", err)
	}
	return cmd.Wait()
}

// resumeProcess resumes the main thread of the process pid, which was
// started suspended. The pid cannot be reused meanwhile, because the
// process handle of cmd is still open.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	resumed := false
	entry := windows.ThreadEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		if resumeThread(pid, entry.ThreadID) {
			resumed = true
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if !resumed {
		return fmt.Errorf("no suspended thread found for process %d", pid)
	}
	return nil
}

// resumeThread resumes the thread tid if it belongs to the process pid. It
// reports whether the thread was suspended once, as the main thread of a
// process that was started suspended is.
func resumeThread(pid, tid uint32) bool {
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION, false, tid)
	if err != nil {
		// the thread may have exited, e.g. one that was injected
		return false
	}
	defer func() { _ = windows.CloseHandle(thread) }()

	// the thread id may have been reused since the snapshot was taken
	if owner, err := getProcessIdOfThread(thread); err != nil || owner != pid {
		return false
	}
	count, err := windows.ResumeThread(thread)
	return err == nil && count == 1
}

// getProcessIdOfThread returns the id of the process that thread belongs to.
func getProcessIdOfThread(thread windows.Handle) (uint32, error) {
	r, _, err := procGetProcessIdOfThread.Call(uintptr(thread))
	if r == 0 {
		return 0, err
	}
	return uint32(r), nil
}
