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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTestCaseConfig(t *testing.T) {
	testCases := map[string]struct {
		config       string
		expectedErr  string
		expectConfig func(t *testing.T, config TestCaseConfig)
	}{
		"single command": {
			config: `
parallel: true
kptArgs: ["live", "apply"]
exitCode: 1
stdErr: |
  error
inventory:
  - kind: ConfigMap
    name: cm
`,
			expectConfig: func(t *testing.T, config TestCaseConfig) {
				assert.Equal(t, []string{"live", "apply"}, config.KptArgs)
				assert.Equal(t, 1, config.ExitCode)
				assert.Equal(t, "error\n", config.StdErr)
				assert.Len(t, config.Inventory, 1)
				assert.Empty(t, config.Steps)
			},
		},
		"empty config": {
			config: ``,
			expectConfig: func(t *testing.T, config TestCaseConfig) {
				assert.Equal(t, TestCaseConfig{}, config)
			},
		},
		"steps": {
			config: `
steps:
  - kptArgs: ["live", "init"]
    stdErr: ""
    files:
      - path: resourcegroup.yaml
        contains: ["kind: ResourceGroup"]
  - kptArgs: ["live", "init"]
    exitCode: 1
    stdErrContains: ["failed"]
`,
			expectConfig: func(t *testing.T, config TestCaseConfig) {
				require.Len(t, config.Steps, 2)
				first, second := config.Steps[0], config.Steps[1]
				require.NotNil(t, first.StdErr)
				assert.Equal(t, "", *first.StdErr)
				assert.Nil(t, first.StdOut)
				assert.Equal(t, []FileCheck{{Path: "resourcegroup.yaml", Contains: []string{"kind: ResourceGroup"}}}, first.Files)
				assert.Equal(t, 1, second.ExitCode)
				assert.Nil(t, second.StdErr)
				assert.Equal(t, []string{"failed"}, second.StdErrContains)
			},
		},
		"unknown field": {
			config:      "kptArgs: [\"live\", \"apply\"]\nstdout: \"\"\n",
			expectedErr: "field stdout not found",
		},
		"unknown field in step": {
			config:      "steps:\n  - kptArgs: [\"live\", \"init\"]\n    stderr: \"\"\n",
			expectedErr: "field stderr not found",
		},
		"more than one document": {
			config:      "kptArgs: [\"live\", \"apply\"]\n---\nkptArgs: [\"live\", \"destroy\"]\n",
			expectedErr: "single YAML document",
		},
		"steps with kptArgs": {
			config:      "kptArgs: [\"live\", \"apply\"]\nsteps:\n  - kptArgs: [\"live\", \"init\"]\n",
			expectedErr: `"kptArgs" can not be used together with "steps"`,
		},
		"steps with zero exitCode": {
			config:      "exitCode: 0\nsteps:\n  - kptArgs: [\"live\", \"init\"]\n",
			expectedErr: `"exitCode" can not be used together with "steps"`,
		},
		"steps with empty stdErr": {
			config:      "stdErr: \"\"\nsteps:\n  - kptArgs: [\"live\", \"init\"]\n",
			expectedErr: `"stdErr" can not be used together with "steps"`,
		},
		"empty steps": {
			config:      "steps: []\n",
			expectedErr: `"steps" must not be empty`,
		},
		"step without kptArgs": {
			config:      "steps:\n  - exitCode: 1\n",
			expectedErr: `step 1: "kptArgs" must be set`,
		},
		"step with null stdErr": {
			config:      "steps:\n  - kptArgs: [\"live\", \"init\"]\n    stdErr:\n",
			expectedErr: `step 1: "stdErr" is null`,
		},
		"file path outside the package": {
			config:      "steps:\n  - kptArgs: [\"live\", \"init\"]\n    files:\n      - path: ../config.yaml\n",
			expectedErr: `step 1: file path "../config.yaml" must be a relative path inside the package`,
		},
		"absolute file path": {
			config:      "steps:\n  - kptArgs: [\"live\", \"init\"]\n    files:\n      - path: /etc/hosts\n",
			expectedErr: `step 1: file path "/etc/hosts" must be a relative path inside the package`,
		},
		"merge key": {
			config:      "steps:\n  - kptArgs: [\"live\", \"init\"]\n    <<: {stdErr: null}\n",
			expectedErr: "anchors, aliases and merge keys are not supported (line 3)",
		},
		"alias": {
			config:      "optionalStdOut: &args [\"live\", \"init\"]\nsteps:\n  - kptArgs: *args\n",
			expectedErr: "anchors, aliases and merge keys are not supported (line 1)",
		},
		"empty file path": {
			config:      "steps:\n  - kptArgs: [\"live\", \"init\"]\n    files:\n      - contains: [\"a\"]\n",
			expectedErr: `step 1: file path "" must be a relative path inside the package`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			config, err := parseTestCaseConfig([]byte(tc.config))
			if tc.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErr)
				return
			}
			require.NoError(t, err)
			tc.expectConfig(t, config)
		})
	}
}

// TestReadExistingTestCaseConfigs makes sure that the configs of all test
// cases in the e2e folder can be read.
func TestReadExistingTestCaseConfigs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "live-*", "*", "config.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			_, err = parseTestCaseConfig(b)
			assert.NoError(t, err)
		})
	}
}
