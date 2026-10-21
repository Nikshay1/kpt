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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	KindClusterName   = "live-e2e-test"
	K8sVersionEnvName = "K8S_VERSION"
)

func InstallResourceGroup(t *testing.T) {
	cmd := exec.Command("kpt", "live", "install-resource-group")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("error installing ResourceGroup CRD: %v\n%s", err, out)
	}
}

func RemoveResourceGroup(t *testing.T) {
	cmd := exec.Command("kubectl", "delete", "crd", "resourcegroups.kpt.dev")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("error removing ResourceGroup CRD: %v\n%s", err, out)
	}
	if CheckIfResourceGroupInstalled(t) {
		t.Fatalf("couldn't remove ResourceGroup CRD")
	}

	// kubectl and kpt cache the API discovery on disk for hours, so they
	// would still find the ResourceGroup type. kubectl api-resources
	// refreshes the cache. Wait until the type is gone from it.
	deadline := time.Now().Add(time.Minute)
	for {
		cmd := exec.Command("kubectl", "api-resources", "--api-group=kpt.dev", "-o", "name")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("error refreshing the API discovery cache: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "resourcegroups.kpt.dev") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ResourceGroup type is still served after the CRD was removed")
		}
		time.Sleep(time.Second)
	}
}

// CheckIfResourceGroupInstalled checks for the ResourceGroup CRD object, so
// the result does not depend on the API discovery cache.
func CheckIfResourceGroupInstalled(t *testing.T) bool {
	cmd := exec.Command("kubectl", "get", "crd", "resourcegroups.kpt.dev")
	output, err := cmd.CombinedOutput()
	if strings.Contains(string(output), "NotFound") {
		return false
	}
	if err != nil {
		t.Fatalf("error checking for ResourceGroup CRD: %v\n%s", err, output)
	}
	return true
}

func CheckKindClusterAvailable(t *testing.T) bool {
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to check for kind cluster: %v", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(output))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == KindClusterName {
			return true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("error parsing output from 'kind get cluster': %v", err)
	}
	return false
}

func CreateKindCluster(t *testing.T) {
	args := []string{"create", "cluster", fmt.Sprintf("--name=%s", KindClusterName), "--wait=2m"}
	if k8sVersion := os.Getenv(K8sVersionEnvName); k8sVersion != "" {
		t.Logf("Using version %s", k8sVersion)
		args = append(args, fmt.Sprintf("--image=kindest/node:v%s", k8sVersion))
	}
	cmd := exec.Command("kind", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create new kind cluster: %v\n%s", err, out)
	}
}

func RemoveKindCluster(t *testing.T) {
	cmd := exec.Command("kind", "delete", "cluster", fmt.Sprintf("--name=%s", KindClusterName))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to remove existing cluster: %v\n%s", err, out)
	}
}

func CreateNamespace(t *testing.T, namespace string) {
	cmd := exec.Command("kubectl", "create", "ns", namespace)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("error creating namespace %s: %v\n%s", namespace, err, out)
	}
}

func RemoveNamespace(t *testing.T, namespace string) {
	cmd := exec.Command("kubectl", "delete", "ns", namespace, "--wait=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("error deleting namespace %s: %v\n%s", namespace, err, out)
	}
}
