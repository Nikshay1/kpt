// Copyright 2021-2026 The kpt Authors
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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"
	"sigs.k8s.io/cli-utils/pkg/testutil"
	"sigs.k8s.io/kustomize/kyaml/copyutil"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

// commandTimeout is how long a kpt command may run. It is longer than the
// largest --reconcile-timeout used by a test case (10m) and shorter than the
// -timeout of the make targets (20m).
const commandTimeout = 15 * time.Minute

// ExecFunc runs a command in dir. The returned error is nil or an
// *exec.ExitError if the command ran, and any other error if it could not
// be run.
type ExecFunc func(ctx context.Context, dir, name string, args ...string) (stdout, stderr string, err error)

// Runner uses the provided Config to run a test.
type Runner struct {
	// Config provides the configuration for how this test should be
	// executed.
	Config TestCaseConfig

	// Path provides the path to the test files.
	Path string

	// Exec runs the kpt commands. Default: run them with os/exec.
	Exec ExecFunc

	// workDir is the copy of Path that the kpt commands run in.
	workDir string

	// timeout overrides commandTimeout in unit tests.
	timeout time.Duration
}

// Run executes the test.
func (r *Runner) Run(t *testing.T) {
	testName := filepath.Base(r.Path)
	r.copyToWorkDir(t)
	r.RunPreApply(t)

	if len(r.Config.Steps) != 0 {
		r.runSteps(t)
		return
	}

	stdout, stderr, exitCode := r.RunApply(t)
	r.VerifyExitCode(t, exitCode)
	r.VerifyStdout(t, stdout)
	r.VerifyStderr(t, stderr)
	if len(r.Config.Inventory) != 0 {
		r.VerifyInventory(t, testName, testName)
	}
}

// copyToWorkDir copies the test files to a temporary directory, so the kpt
// commands can change the package without changing the test files.
func (r *Runner) copyToWorkDir(t *testing.T) {
	r.workDir = filepath.Join(t.TempDir(), filepath.Base(r.Path))
	if err := copyutil.CopyDir(filesys.MakeFsOnDisk(), r.Path, r.workDir); err != nil {
		t.Fatalf("error copying %s to %s: %v", r.Path, r.workDir, err)
	}
}

func (r *Runner) packageDir() string {
	return filepath.Join(r.workDir, "resources")
}

func (r *Runner) RunPreApply(t *testing.T) {
	preApplyDir := filepath.Join(r.Path, "pre-apply")
	fi, err := os.Stat(preApplyDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("error checking for pre-apply dir: %v", err)
	}
	if os.IsNotExist(err) || !fi.IsDir() {
		return
	}
	t.Log("Applying resources in pre-apply directory")
	cmd := exec.Command("kubectl", "apply", "-f", preApplyDir)
	if err := cmd.Run(); err != nil {
		t.Fatalf("error applying pre-apply dir: %v", err)
	}
}

func (r *Runner) RunApply(t *testing.T) (string, string, int) {
	t.Logf("Running command: kpt %s", strings.Join(r.Config.KptArgs, " "))
	stdout, stderr, exitCode, err := r.runKpt(r.Config.KptArgs)
	if err != nil {
		t.Fatal(err)
	}
	return stdout, stderr, exitCode
}

// runKpt runs kpt with args in the package. It returns an error if kpt
// could not be run or did not finish in time.
func (r *Runner) runKpt(args []string) (string, string, int, error) {
	timeout := r.timeout
	if timeout == 0 {
		timeout = commandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	run := r.Exec
	if run == nil {
		run = execCommand
	}
	stdout, stderr, err := run(ctx, r.packageDir(), "kpt", args...)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout, stderr, 0, fmt.Errorf("kpt %s did not finish within %s", strings.Join(args, " "), timeout)
	}
	if err == nil {
		return stdout, stderr, 0, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return stdout, stderr, exitErr.ExitCode(), nil
	}
	return stdout, stderr, 0, fmt.Errorf("error running kpt %s: %w", strings.Join(args, " "), err)
}

func execCommand(ctx context.Context, dir, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// runSteps runs the steps of the test case one after the other. It stops
// at the first step that fails.
func (r *Runner) runSteps(t *testing.T) {
	normalize := func(text string) string {
		return r.prepOutput(t, text)
	}
	readFile := func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(r.packageDir(), path))
	}
	for i, step := range r.Config.Steps {
		t.Logf("Step %d: kpt %s", i+1, strings.Join(step.KptArgs, " "))
		stdout, stderr, exitCode, err := r.runKpt(step.KptArgs)
		if err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
		failures := checkStep(step, stdout, stderr, exitCode, normalize, readFile)
		if len(failures) == 0 {
			continue
		}
		for _, failure := range failures {
			t.Errorf("step %d: %s", i+1, failure)
		}
		t.Logf("step %d stdout:\n%s", i+1, stdout)
		t.Logf("step %d stderr:\n%s", i+1, stderr)
		t.FailNow()
	}
}

// checkStep returns a description of every expectation of the step that
// the command result does not meet.
func checkStep(step Step, stdout, stderr string, exitCode int,
	normalize func(string) string, readFile func(string) ([]byte, error)) []string {
	var failures []string
	if step.ExitCode != exitCode {
		failures = append(failures, fmt.Sprintf("expected exit code %d, but got %d", step.ExitCode, exitCode))
	}
	failures = append(failures, checkOutput("stdout", normalize(stdout),
		step.StdOut, step.StdOutContains, step.StdOutNotContains)...)
	failures = append(failures, checkOutput("stderr", normalize(stderr),
		step.StdErr, step.StdErrContains, step.StdErrNotContains)...)
	for _, file := range step.Files {
		b, err := readFile(file.Path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("error reading file %s: %v", file.Path, err))
			continue
		}
		failures = append(failures, checkText("file "+file.Path, string(b), file.Contains, file.NotContains)...)
	}
	return failures
}

func checkOutput(name, output string, expected *string, contains, notContains []string) []string {
	var failures []string
	if expected != nil && strings.TrimSpace(*expected) != output {
		failures = append(failures, fmt.Sprintf("expected %s:\n%s\nbut got:\n%s",
			name, strings.TrimSpace(*expected), output))
	}
	return append(failures, checkText(name, output, contains, notContains)...)
}

func checkText(name, text string, contains, notContains []string) []string {
	var failures []string
	for _, s := range contains {
		if !strings.Contains(text, s) {
			failures = append(failures, fmt.Sprintf("%s does not contain %q", name, s))
		}
	}
	for _, s := range notContains {
		if strings.Contains(text, s) {
			failures = append(failures, fmt.Sprintf("%s contains %q", name, s))
		}
	}
	return failures
}

func (r *Runner) VerifyExitCode(t *testing.T, exitCode int) {
	if want, got := r.Config.ExitCode, exitCode; want != got {
		t.Errorf("expected exit code %d, but got %d", want, got)
	}
}

func (r *Runner) VerifyStdout(t *testing.T, stdout string) {
	testutil.AssertEqual(t, strings.TrimSpace(r.Config.StdOut), r.prepOutput(t, stdout))
}

func (r *Runner) VerifyStderr(t *testing.T, stderr string) {
	testutil.AssertEqual(t, strings.TrimSpace(r.Config.StdErr), r.prepOutput(t, stderr))
}

func (r *Runner) prepOutput(t *testing.T, txt string) string {
	txt = removeStatusEvents(t, txt)
	txt = substituteTimestamps(txt)
	txt = substituteUIDs(txt)
	txt = substituteResourceVersion(txt)
	txt = r.removeOptionalEvents(t, txt)
	txt = removeClientSideThrottlingEvents(t, txt)
	return strings.TrimSpace(txt)
}

func (r *Runner) VerifyInventory(t *testing.T, name, namespace string) {
	rgExec := exec.Command("kubectl", "get", "resourcegroups.kpt.dev",
		"-n", namespace, name, "-oyaml")
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	rgExec.Stdout = &outBuf
	rgExec.Stderr = &errBuf
	err := rgExec.Run()
	if strings.Contains(errBuf.String(), "NotFound") {
		t.Errorf("inventory with namespace %s and name %s not found",
			namespace, name)
		return
	}
	if err != nil {
		t.Fatalf("error looking up resource group: %v", err)
	}
	var rg map[string]any
	err = yaml.Unmarshal(outBuf.Bytes(), &rg)
	if err != nil {
		t.Fatalf("error unmarshalling inventory object: %v", err)
	}

	var inventory []InventoryEntry
	if rg["spec"] != nil {
		spec := rg["spec"].(map[string]any)
		if spec["resources"] != nil {
			resources := spec["resources"].([]any)
			for i := range resources {
				r := resources[i].(map[string]any)
				inventory = append(inventory, InventoryEntry{
					Group:     r["group"].(string),
					Kind:      r["kind"].(string),
					Name:      r["name"].(string),
					Namespace: r["namespace"].(string),
				})
			}
		}
	}

	expectedInventory := r.Config.Inventory
	sort.Slice(expectedInventory, inventorySortFunc(expectedInventory))
	sort.Slice(inventory, inventorySortFunc(inventory))

	assert.Equal(t, expectedInventory, inventory)
}

func inventorySortFunc(inv []InventoryEntry) func(i, j int) bool {
	return func(i, j int) bool {
		iInv := inv[i]
		jInv := inv[j]

		if iInv.Group != jInv.Group {
			return iInv.Group < jInv.Group
		}
		if iInv.Kind != jInv.Kind {
			return iInv.Kind < jInv.Kind
		}
		if iInv.Name != jInv.Name {
			return iInv.Name < jInv.Name
		}
		return iInv.Namespace < jInv.Namespace
	}
}

var timestampRegexp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`)

func substituteTimestamps(text string) string {
	return timestampRegexp.ReplaceAllString(text, "<TIMESTAMP>")
}

var uidRegexp = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func substituteUIDs(text string) string {
	return uidRegexp.ReplaceAllLiteralString(text, "<UID>")
}

var resourceVersionRegexp = regexp.MustCompile(`resourceVersion: "[0-9]+"`)

func substituteResourceVersion(text string) string {
	return resourceVersionRegexp.ReplaceAllLiteralString(text, "resourceVersion: \"<RV>\"")
}

// statuses is a list of all status enums from the kstatus library.
var statuses = []string{
	status.InProgressStatus.String(),
	status.CurrentStatus.String(),
	status.FailedStatus.String(),
	status.TerminatingStatus.String(),
	status.UnknownStatus.String(),
	status.NotFoundStatus.String(),
}

// removeStatusEvents removes all lines from the input string that match the
// StatusEvent printer output from the cli-utils event printer.
func removeStatusEvents(t *testing.T, text string) string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	var lines []string

	// Match StatusEvent printer output
	// https://github.com/kubernetes-sigs/cli-utils/blob/master/pkg/printers/events/formatter.go#L166
	statusMatchStr := strings.Join(statuses, "|")
	pattern := regexp.MustCompile(fmt.Sprintf("^(.*)/(.*) is (%s): (.*)", statusMatchStr))

	for scanner.Scan() {
		line := scanner.Text()
		if pattern.MatchString(line) {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("error scanning output: %v", err)
	}
	return strings.Join(lines, "\n")
}

// removeClientSideThrottlingEvents removes all lines from the input string that
// match the client-side throttling log message from the client-go RESTClient.
func removeClientSideThrottlingEvents(t *testing.T, text string) string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	var lines []string

	// Match RESTClient's client-side throtting error, which gets logged at
	// level 1 if the delay is longer than 1s, and level 3 otherwise.
	// https://github.com/kubernetes/client-go/blob/v0.24.0/rest/request.go#L529
	pattern := regexp.MustCompile(".* Waited for .* due to client-side throttling, not priority and fairness, request: .*")

	for scanner.Scan() {
		line := scanner.Text()
		if pattern.MatchString(line) {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("error scanning output: %v", err)
	}
	return strings.Join(lines, "\n")
}

// removeOptionalEvents removes all lines from the input string that exactly
// match entries in the Runner.Config.OptionalStdOut list
func (r *Runner) removeOptionalEvents(t *testing.T, text string) string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	var lines []string

	for scanner.Scan() {
		line := scanner.Text()
		if equalsAny(line, r.Config.OptionalStdOut) {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("error scanning output: %v", err)
	}
	return strings.Join(lines, "\n")
}

func equalsAny(s string, strs []string) bool {
	return slices.Contains(strs, s)
}
