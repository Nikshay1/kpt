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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/kyaml/yaml"
)

type TestCaseConfig struct {
	// ExitCode is the expected exit code from the kpt commands. Default: 0
	ExitCode int `yaml:"exitCode,omitempty"`

	// StdErr is the expected standard error output. Default: ""
	StdErr string `yaml:"stdErr,omitempty"`

	// StdOut is the expected standard output from running the command.
	// Default: ""
	StdOut string `yaml:"stdOut,omitempty"`

	// OptionalStdOut is a list of lines that are optional in the standard
	// output from running the command.
	// Default: nil
	OptionalStdOut []string `yaml:"optionalStdOut,omitempty"`

	// Inventory is the expected list of resource present in the inventory.
	Inventory []InventoryEntry `yaml:"inventory,omitempty"`

	// NoResourceGroup defines whether the RG CRD should be present in the cluster
	// when the test starts.
	NoResourceGroup bool `yaml:"noResourceGroup,omitempty"`

	// Parallel defines whether the test can be run in parallel with other
	// tests. The primary requirement here is that the test doesn't create,
	// update, or delete any cluster-scoped resources.
	Parallel bool `yaml:"parallel,omitempty"`

	// KptArgs is a list of args that will be provided to the kpt command
	// when running the test.
	KptArgs []string `yaml:"kptArgs,omitempty"`

	// Steps is a list of kpt commands that are run one after the other
	// in the same copy of the package. It can not be used together with
	// KptArgs, ExitCode, StdOut, StdErr and Inventory.
	Steps []Step `yaml:"steps,omitempty"`
}

// Step is one kpt command in a test case and what is expected from it.
type Step struct {
	// KptArgs is a list of args that will be provided to the kpt command.
	KptArgs []string `yaml:"kptArgs,omitempty"`

	// ExitCode is the expected exit code of the command. Default: 0
	ExitCode int `yaml:"exitCode,omitempty"`

	// StdOut is the expected standard output. If it is not set, the
	// standard output is not checked. An empty string means that the
	// output must be empty.
	StdOut *string `yaml:"stdOut,omitempty"`

	// StdErr is the expected standard error output. If it is not set, the
	// standard error output is not checked. An empty string means that the
	// output must be empty.
	StdErr *string `yaml:"stdErr,omitempty"`

	// StdOutContains is a list of strings that must be in the standard
	// output.
	StdOutContains []string `yaml:"stdOutContains,omitempty"`

	// StdOutNotContains is a list of strings that must not be in the
	// standard output.
	StdOutNotContains []string `yaml:"stdOutNotContains,omitempty"`

	// StdErrContains is a list of strings that must be in the standard
	// error output.
	StdErrContains []string `yaml:"stdErrContains,omitempty"`

	// StdErrNotContains is a list of strings that must not be in the
	// standard error output.
	StdErrNotContains []string `yaml:"stdErrNotContains,omitempty"`

	// Files is a list of checks on files in the package after the command
	// has run.
	Files []FileCheck `yaml:"files,omitempty"`
}

// FileCheck defines what a file in the package must and must not contain.
type FileCheck struct {
	// Path is the path of the file, relative to the package.
	Path string `yaml:"path,omitempty"`

	// Contains is a list of strings that must be in the file.
	Contains []string `yaml:"contains,omitempty"`

	// NotContains is a list of strings that must not be in the file.
	NotContains []string `yaml:"notContains,omitempty"`
}

// InventoryEntry defines an entry in an inventory list.
type InventoryEntry struct {
	Group     string `yaml:"group,omitempty"`
	Kind      string `yaml:"kind,omitempty"`
	Name      string `yaml:"name,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
}

func ReadTestCaseConfig(t *testing.T, path string) TestCaseConfig {
	configPath := filepath.Join(path, "config.yaml")
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("unable to read test config at %s", configPath)
	}

	config, err := parseTestCaseConfig(b)
	if err != nil {
		t.Fatalf("unable to unmarshal test config file %s: %v", configPath, err)
	}
	return config
}

// singleCommandFields are the fields of TestCaseConfig that can not be
// used together with steps.
var singleCommandFields = []string{"kptArgs", "exitCode", "stdOut", "stdErr", "inventory"}

func parseTestCaseConfig(b []byte) (TestCaseConfig, error) {
	var config TestCaseConfig
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		if errors.Is(err, io.EOF) {
			return config, nil
		}
		return config, err
	}
	var next yaml.Node
	if err := decoder.Decode(&next); !errors.Is(err, io.EOF) {
		if err != nil {
			return config, err
		}
		return config, fmt.Errorf("the config must be a single YAML document")
	}

	// The struct can not tell an omitted field from a field set to its
	// zero value, so the keys are read from the YAML nodes.
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return config, err
	}
	if len(doc.Content) == 0 {
		return config, nil
	}
	if err := checkNoAliases(&doc); err != nil {
		return config, err
	}
	keys := mappingValues(doc.Content[0])
	stepsNode, found := keys["steps"]
	if !found {
		return config, nil
	}
	for _, field := range singleCommandFields {
		if _, found := keys[field]; found {
			return config, fmt.Errorf("%q can not be used together with \"steps\"", field)
		}
	}
	if len(config.Steps) == 0 {
		return config, fmt.Errorf("\"steps\" must not be empty")
	}
	for i, step := range config.Steps {
		if err := validateStep(step, mappingValues(stepsNode.Content[i])); err != nil {
			return config, fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return config, nil
}

func validateStep(step Step, keys map[string]*yaml.Node) error {
	if len(step.KptArgs) == 0 {
		return fmt.Errorf("\"kptArgs\" must be set")
	}
	for _, field := range []string{"stdOut", "stdErr"} {
		if node, found := keys[field]; found && node.Tag == "!!null" {
			return fmt.Errorf("%q is null; use \"\" to expect empty output", field)
		}
	}
	for _, file := range step.Files {
		if file.Path == "" || filepath.IsAbs(file.Path) ||
			slices.Contains(strings.Split(filepath.ToSlash(file.Path), "/"), "..") {
			return fmt.Errorf("file path %q must be a relative path inside the package", file.Path)
		}
	}
	return nil
}

// mappingValues returns the value nodes of a YAML mapping node by key.
func mappingValues(node *yaml.Node) map[string]*yaml.Node {
	values := make(map[string]*yaml.Node)
	if node == nil || node.Kind != yaml.MappingNode {
		return values
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		values[node.Content[i].Value] = node.Content[i+1]
	}
	return values
}

// checkNoAliases returns an error if the YAML uses anchors, aliases or merge
// keys. The checks on the YAML nodes would not see the values they bring in.
func checkNoAliases(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return fmt.Errorf("anchors, aliases and merge keys are not supported (line %d)", node.Line)
	}
	for _, child := range node.Content {
		if err := checkNoAliases(child); err != nil {
			return err
		}
	}
	return nil
}
