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

package live

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(s string) *string {
	return &s
}

func TestCheckStep(t *testing.T) {
	files := map[string]string{
		"resourcegroup.yaml": "kind: ResourceGroup\nname: inventory-1\n",
	}
	readFile := func(path string) ([]byte, error) {
		content, found := files[path]
		if !found {
			return nil, os.ErrNotExist
		}
		return []byte(content), nil
	}

	testCases := map[string]struct {
		step             Step
		stdout           string
		stderr           string
		exitCode         int
		expectedFailures []string
	}{
		"no expectations": {
			step:   Step{KptArgs: []string{"live", "init"}},
			stdout: "out",
			stderr: "err",
		},
		"exit code": {
			step:             Step{ExitCode: 1},
			expectedFailures: []string{"expected exit code 1, but got 0"},
		},
		"exact output": {
			step:   Step{StdOut: ptr("out\n"), StdErr: ptr("err\n")},
			stdout: "out\n",
			stderr: "err\n",
		},
		"exact output differs": {
			step:             Step{StdErr: ptr("other\n")},
			stderr:           "err\n",
			expectedFailures: []string{"expected stderr:\nother\nbut got:\nerr"},
		},
		"empty output expected": {
			step:             Step{StdOut: ptr(""), StdErr: ptr("")},
			stderr:           "err",
			expectedFailures: []string{"expected stderr:\n\nbut got:\nerr"},
		},
		"contains": {
			step: Step{
				StdOutContains:    []string{"a", "b"},
				StdOutNotContains: []string{"c"},
				StdErrContains:    []string{"x"},
				StdErrNotContains: []string{"y"},
			},
			stdout: "a c",
			stderr: "y",
			expectedFailures: []string{
				`stdout does not contain "b"`,
				`stdout contains "c"`,
				`stderr does not contain "x"`,
				`stderr contains "y"`,
			},
		},
		"files": {
			step: Step{Files: []FileCheck{
				{Path: "resourcegroup.yaml", Contains: []string{"kind: ResourceGroup", "namespace: init"}, NotContains: []string{"inventory-1"}},
				{Path: "missing.yaml"},
			}},
			expectedFailures: []string{
				`file resourcegroup.yaml does not contain "namespace: init"`,
				`file resourcegroup.yaml contains "inventory-1"`,
				"error reading file missing.yaml: " + os.ErrNotExist.Error(),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			failures := checkStep(tc.step, tc.stdout, tc.stderr, tc.exitCode, strings.TrimSpace, readFile)
			assert.Equal(t, tc.expectedFailures, failures)
		})
	}
}

func TestRunKpt(t *testing.T) {
	testCases := map[string]struct {
		exec             ExecFunc
		timeout          time.Duration
		expectedExitCode int
		expectedErr      string
	}{
		"success": {
			exec: func(context.Context, string, string, ...string) (string, string, error) {
				return "out", "err", nil
			},
		},
		"non-zero exit code": {
			exec: func(ctx context.Context, _, _ string, _ ...string) (string, string, error) {
				return "", "", exec.CommandContext(ctx, "sh", "-c", "exit 3").Run()
			},
			expectedExitCode: 3,
		},
		"command can not be started": {
			exec: func(context.Context, string, string, ...string) (string, string, error) {
				return "", "", errors.New("executable file not found")
			},
			expectedErr: "error running kpt live apply: executable file not found",
		},
		"timeout": {
			exec: func(ctx context.Context, _, _ string, _ ...string) (string, string, error) {
				<-ctx.Done()
				return "", "", ctx.Err()
			},
			timeout:     10 * time.Millisecond,
			expectedErr: "kpt live apply did not finish within 10ms",
		},
		"timeout kills the command": {
			// The killed command returns an *exec.ExitError, which must be
			// reported as a timeout and not as an exit code.
			exec: func(ctx context.Context, _, _ string, _ ...string) (string, string, error) {
				return "", "", exec.CommandContext(ctx, "sh", "-c", "sleep 5").Run()
			},
			timeout:     200 * time.Millisecond,
			expectedErr: "kpt live apply did not finish within 200ms",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := &Runner{Exec: tc.exec, timeout: tc.timeout}
			_, _, exitCode, err := r.runKpt([]string{"live", "apply"})
			if tc.expectedErr != "" {
				require.Error(t, err)
				assert.Equal(t, tc.expectedErr, err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expectedExitCode, exitCode)
		})
	}
}

// TestRunUsesCopy checks that the kpt commands run in a copy of the test
// case, so they can not change the test files.
func TestRunUsesCopy(t *testing.T) {
	testCase := filepath.Join(t.TempDir(), "my-test")
	require.NoError(t, os.MkdirAll(filepath.Join(testCase, "resources"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(testCase, "resources", "Kptfile"), []byte("kind: Kptfile\n"), 0600))

	var dirs []string
	writeFile := func(_ context.Context, dir, _ string, args ...string) (string, string, error) {
		dirs = append(dirs, dir)
		name := args[len(args)-1]
		return "", "", os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0600)
	}

	t.Run("single command", func(t *testing.T) {
		dirs = nil
		r := &Runner{
			Config: TestCaseConfig{KptArgs: []string{"live", "init", "rg.yaml"}},
			Path:   testCase,
			Exec:   writeFile,
		}
		r.Run(t)

		require.Len(t, dirs, 1)
		assert.Equal(t, filepath.Join(r.workDir, "resources"), dirs[0])
		assert.Equal(t, "my-test", filepath.Base(r.workDir))
		assert.NotEqual(t, testCase, r.workDir)
		assert.FileExists(t, filepath.Join(dirs[0], "Kptfile"))
		assert.NoFileExists(t, filepath.Join(testCase, "resources", "rg.yaml"))
	})

	t.Run("steps", func(t *testing.T) {
		dirs = nil
		r := &Runner{
			Config: TestCaseConfig{Steps: []Step{
				{KptArgs: []string{"live", "init", "first.yaml"}},
				{
					KptArgs: []string{"live", "init", "second.yaml"},
					Files: []FileCheck{
						{Path: "first.yaml", Contains: []string{"first.yaml"}},
						{Path: "second.yaml", Contains: []string{"second.yaml"}},
					},
				},
			}},
			Path: testCase,
			Exec: writeFile,
		}
		r.Run(t)

		require.Len(t, dirs, 2)
		assert.Equal(t, dirs[0], dirs[1])
		assert.NoFileExists(t, filepath.Join(testCase, "resources", "first.yaml"))
	})
}
