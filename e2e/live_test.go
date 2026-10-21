//go:build kind
// +build kind

// Copyright 2021, 2026 The kpt Authors
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

package e2e

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	livetest "github.com/kptdev/kpt/e2e/framework/live"
	"github.com/stretchr/testify/require"
)

func TestLiveApply(t *testing.T) {
	runTests(t, filepath.Join(".", "testdata", "live-apply"))
}

func TestLivePlan(t *testing.T) {
	runTests(t, filepath.Join(".", "testdata", "live-plan"))
}

func TestLiveScenarios(t *testing.T) {
	runTests(t, filepath.Join(".", "testdata", "live-scenarios"))
}

var (
	clusterOnce  sync.Once
	clusterReady bool
)

// ensureCluster creates a new kind cluster the first time it is called in
// the test binary. The cluster is not removed after the tests.
func ensureCluster(t *testing.T) {
	clusterOnce.Do(func() {
		livetest.RemoveKindCluster(t)
		livetest.CreateKindCluster(t)
		clusterReady = true
	})
	if !clusterReady {
		t.Fatal("the kind cluster could not be created")
	}
}

func runTests(t *testing.T, path string) {
	testCases := scanTestCases(t, path)
	ensureCluster(t)
	paths := slices.Sorted(maps.Keys(testCases))

	// Test cases that can not run in parallel run first, one at a time,
	// because they may remove the ResourceGroup CRD.
	for _, p := range paths {
		if c := testCases[p]; !c.Parallel {
			runTestCase(t, p, c)
		}
	}

	// Parallel test cases only start after this function has returned, so
	// the ResourceGroup CRD is installed once for all of them here.
	livetest.InstallResourceGroup(t)
	for _, p := range paths {
		if c := testCases[p]; c.Parallel {
			runTestCase(t, p, c)
		}
	}
}

func runTestCase(t *testing.T, p string, c livetest.TestCaseConfig) {
	t.Run(p, func(t *testing.T) {
		switch {
		case c.Parallel:
			require.False(t, c.NoResourceGroup, "Parallel tests can not modify the RG CRD")
			t.Parallel()
		case c.NoResourceGroup:
			if livetest.CheckIfResourceGroupInstalled(t) {
				livetest.RemoveResourceGroup(t)
			}
		default:
			livetest.InstallResourceGroup(t)
		}

		ns := filepath.Base(p)
		livetest.CreateNamespace(t, ns)
		defer livetest.RemoveNamespace(t, ns)

		(&livetest.Runner{
			Config: c,
			Path:   p,
		}).Run(t)
	})
}

func scanTestCases(t *testing.T, path string) map[string]livetest.TestCaseConfig {
	testCases := make(map[string]livetest.TestCaseConfig)
	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			return nil
		}
		if path == p {
			return nil
		}

		testCases[p] = livetest.ReadTestCaseConfig(t, p)
		return filepath.SkipDir
	})
	if err != nil {
		t.Fatalf("failed to scan for test cases in %s", path)
	}
	return testCases
}
