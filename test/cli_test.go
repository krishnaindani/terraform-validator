// Copyright 2019 Google LLC
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

package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/terraform-validator/converters/google"
)

// TestCLI tests the "convert" and "validate" subcommand against a generated .tfplan file.
func TestCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode.")
		return
	}
	// Define the reusable constraints to be use for the test cases.
	type constraint struct {
		name            string
		wantViolation   bool
		wantOutputRegex string
	}
	// Currently, we only test one rule. Moving forward, resource specific rules
	// should be added to increase the coverage.
	alwaysViolate := constraint{name: "always_violate", wantViolation: true, wantOutputRegex: "Constraint GCPAlwaysViolatesConstraintV1.always_violates_all on resource"}

	// Test cases for each type of resource is defined here.
	cases := []struct {
		name                 string
		constraints          []constraint
		compareConvertOutput compareConvertOutputFunc
	}{
		{name: "bucket"},
		{name: "disk"},
		{name: "firewall"},
		{name: "instance"},
		{name: "sql"},
		{name: "example_bigquery_dataset"},
		{name: "example_bigtable_instance"},
		{name: "example_compute_disk"},
		{name: "example_compute_firewall"},
		{name: "example_compute_forwarding_rule"},
		{name: "example_compute_instance"},
		{name: "example_compute_network"},
		{name: "example_compute_subnetwork"},
		{name: "example_compute_global_forwarding_rule"},
		{name: "example_container_cluster"},
		{name: "example_filestore_instance"},
		{name: "example_kms_crypto_key"},
		{name: "example_kms_key_ring"},
		{name: "example_organization_iam_binding"},
		{name: "example_organization_iam_member"},
		{name: "example_organization_iam_policy"},
		{name: "example_pubsub_topic"},
		{name: "example_pubsub_subscription"},
		{name: "example_project_create", constraints: []constraint{alwaysViolate, constraint{name: "project_match_target", wantViolation: false, wantOutputRegex: ""}}},
		{name: "example_project_update", constraints: []constraint{alwaysViolate, constraint{name: "project_match_target", wantViolation: true, wantOutputRegex: "Constraint GCPAlwaysViolatesConstraintV1.always_violates_project_match_target on resource"}}},
		{name: "example_project_in_org"},
		{name: "example_project_in_folder"},
		{name: "example_project_iam"},
		{name: "example_project_iam_binding", compareConvertOutput: compareMergedIamBindingOutput},
		{name: "example_project_iam_member", compareConvertOutput: compareMergedIamMemberOutput},
		{name: "example_project_iam_policy"},
		{name: "example_project_organization_policy"},
		{name: "example_project_service"},
		{name: "example_sql_database_instance"},
		{name: "example_storage_bucket"},
		{name: "example_storage_bucket_iam_binding", compareConvertOutput: compareMergedIamBindingOutput},
		{name: "example_storage_bucket_iam_member", compareConvertOutput: compareMergedIamMemberOutput},
		{name: "example_storage_bucket_iam_member_random_suffix"},
		{name: "example_storage_bucket_iam_policy"},
		{name: "full_compute_firewall"},
		{name: "full_compute_instance"},
		{name: "full_container_cluster"},
		{name: "full_container_node_pool"},
		{name: "full_sql_database_instance"},
		{name: "full_storage_bucket"},
	}

	// Map of cases to skip to reasons for the skip
	skipCases := map[string]string{
		"TestCLI/v=0.12/tf=example_compute_forwarding_rule/offline=true/cmd=convert":                              "temperarily skip because of the predictable drift in offline mode",
		"TestCLI/v=0.12/tf=example_compute_forwarding_rule/offline=true/cmd=validate/constraint=always_violate":   "temperarily skip because of the predictable drift in offline mode",
		"TestCLI/v=0.12/tf=example_compute_instance/offline=true/cmd=convert":                                     "compute_instance doesn't work in offline mode - github.com/hashicorp/terraform-provider-google/issues/8489",
		"TestCLI/v=0.12/tf=example_compute_instance/offline=true/cmd=validate/constraint=always_violate":          "compute_instance doesn't work in offline mode - github.com/hashicorp/terraform-provider-google/issues/8489",
		"TestCLI/v=0.12/tf=example_organization_iam_binding/offline=false/cmd=convert":                            "skip because test runner doesn't have org permissions",
		"TestCLI/v=0.12/tf=example_organization_iam_binding/offline=false/cmd=validate/constraint=always_violate": "skip because test runner doesn't have org permissions",
		"TestCLI/v=0.12/tf=example_organization_iam_member/offline=false/cmd=convert":                             "skip because test runner doesn't have org permissions",
		"TestCLI/v=0.12/tf=example_organization_iam_member/offline=false/cmd=validate/constraint=always_violate":  "skip because test runner doesn't have org permissions",
		"TestCLI/v=0.12/tf=example_project_iam/offline=false/cmd=convert":                                         "example_project_iam is too complex to untangle merges with online data generically",
		"TestCLI/v=0.12/tf=example_storage_bucket_iam_member_random_suffix/offline=false":                         "test produces inconsistent results based on randomized names - github.com/GoogleCloudPlatform/terraform-validator/issues/259",
		"TestCLI/v=0.12/tf=example_storage_bucket_iam_member_random_suffix/offline=false/cmd=convert":             "test produces inconsistent results based on randomized names - github.com/GoogleCloudPlatform/terraform-validator/issues/259",
	}
	for i := range cases {
		// Allocate a variable to make sure test can run in parallel.
		c := cases[i]
		// Add default constraints if not set.
		if len(c.constraints) == 0 {
			c.constraints = []constraint{alwaysViolate}
		}

		// Add default convert comparison func if not set
		if c.compareConvertOutput == nil {
			c.compareConvertOutput = compareUnmergedConvertOutput
		}

		// Test both offline and online mode.
		for _, offline := range []bool{true, false} {
			offline := offline
			t.Run(fmt.Sprintf("v=0.12/tf=%s/offline=%t", c.name, offline), func(t *testing.T) {
				t.Parallel()
				// Create a temporary directory for running terraform.
				dir, err := ioutil.TempDir(tmpDir, "terraform")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(dir)

				// Generate the <name>.tf and <name>_assets.json files into the temporary directory.
				generateTestFiles(t, "../testdata/templates", dir, c.name+".tf")
				generateTestFiles(t, "../testdata/templates", dir, c.name+".json")

				// Uses glob matching to match generateTestFiles internals.
				tfstateMatches, err := filepath.Glob(filepath.Join("../testdata/templates", c.name+".tfstate"))
				if err != nil {
					t.Fatalf("malformed glob: %v", err)
				}
				if tfstateMatches != nil {
					generateTestFiles(t, "../testdata/templates", dir, c.name+".tfstate")
					err = os.Rename(
						filepath.Join(dir, c.name+".tfstate"),
						filepath.Join(dir, "terraform.tfstate"),
					)
					if err != nil {
						t.Fatalf("renaming tfstate: %v", err)
					}
				}

				terraform(t, dir, c.name)

				t.Run("cmd=convert", func(t *testing.T) {
					if reason, exists := skipCases[t.Name()]; exists {
						t.Skip(reason)
					}
					testConvertCommand(t, dir, c.name, offline, c.compareConvertOutput)
				})

				for _, ct := range c.constraints {
					t.Run(fmt.Sprintf("cmd=validate/constraint=%s", ct.name), func(t *testing.T) {
						if reason, exists := skipCases[t.Name()]; exists {
							t.Skip(reason)
						}
						testValidateCommand(t, ct.wantViolation, ct.wantOutputRegex, dir, c.name, offline, ct.name)
					})
				}
			})
		}
	}
}

type compareConvertOutputFunc func(t *testing.T, expected []google.Asset, actual []google.Asset, offline bool)

func compareUnmergedConvertOutput(t *testing.T, expected []google.Asset, actual []google.Asset, offline bool) {
	expectedJSON := normalizeAssets(t, expected, offline)
	actualJSON := normalizeAssets(t, actual, offline)
	require.JSONEq(t, string(expectedJSON), string(actualJSON))
}

// For merged IAM members, only consider whether the expected members are present.
func compareMergedIamMemberOutput(t *testing.T, expected []google.Asset, actual []google.Asset, offline bool) {
	var normalizedActual []google.Asset
	for i := range expected {
		expectedAsset := expected[i]
		actualAsset := actual[i]

		// Copy actualAsset
		normalizedActualAsset := actualAsset

		expectedBindings := map[string]map[string]struct{}{}
		for _, binding := range expectedAsset.IAMPolicy.Bindings {
			expectedBindings[binding.Role] = map[string]struct{}{}
			for _, member := range binding.Members {
				expectedBindings[binding.Role][member] = struct{}{}
			}
		}

		iamPolicy := google.IAMPolicy{}
		for _, binding := range actualAsset.IAMPolicy.Bindings {
			if expectedMembers, exists := expectedBindings[binding.Role]; exists {
				iamBinding := google.IAMBinding{
					Role: binding.Role,
				}
				for _, member := range binding.Members {
					if _, exists := expectedMembers[member]; exists {
						iamBinding.Members = append(iamBinding.Members, member)
					}
				}
				iamPolicy.Bindings = append(iamPolicy.Bindings, iamBinding)
			}
		}
		normalizedActualAsset.IAMPolicy = &iamPolicy
		normalizedActual = append(normalizedActual, normalizedActualAsset)
	}

	expectedJSON := normalizeAssets(t, expected, offline)
	actualJSON := normalizeAssets(t, normalizedActual, offline)
	require.JSONEq(t, string(expectedJSON), string(actualJSON))
}

// For merged IAM bindings, only consider whether the expected bindings are as expected.
func compareMergedIamBindingOutput(t *testing.T, expected []google.Asset, actual []google.Asset, offline bool) {
	var normalizedActual []google.Asset
	for i := range expected {
		expectedAsset := expected[i]
		actualAsset := actual[i]

		// Copy actualAsset
		normalizedActualAsset := actualAsset

		expectedBindings := map[string]struct{}{}
		for _, binding := range expectedAsset.IAMPolicy.Bindings {
			expectedBindings[binding.Role] = struct{}{}
		}

		iamPolicy := google.IAMPolicy{}
		for _, binding := range actualAsset.IAMPolicy.Bindings {
			if _, exists := expectedBindings[binding.Role]; exists {
				iamPolicy.Bindings = append(iamPolicy.Bindings, binding)
			}
		}
		normalizedActualAsset.IAMPolicy = &iamPolicy
		normalizedActual = append(normalizedActual, normalizedActualAsset)
	}

	expectedJSON := normalizeAssets(t, expected, offline)
	actualJSON := normalizeAssets(t, normalizedActual, offline)
	require.JSONEq(t, string(expectedJSON), string(actualJSON))
}

func testConvertCommand(t *testing.T, dir, name string, offline bool, compare compareConvertOutputFunc) {
	var payload []byte

	// Load expected assets
	testfile := filepath.Join(dir, name+".json")
	payload, err := ioutil.ReadFile(testfile)
	if err != nil {
		t.Fatalf("Error reading %v: %v", testfile, err)
	}
	var expected []google.Asset
	if err := json.Unmarshal(payload, &expected); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}

	// Get converted assets
	payload = tfvConvert(t, dir, name+".tfplan.json", offline)
	var actual []google.Asset
	err = json.Unmarshal(payload, &actual)
	if err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}

	compare(t, expected, actual, offline)
}

func testValidateCommand(t *testing.T, wantViolation bool, want, dir, name string, offline bool, constraintName string) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot get current directory: %v", err)
	}
	policyPath := filepath.Join(cwd, samplePolicyPath, constraintName)
	var got []byte
	got = tfvValidate(t, wantViolation, dir, name+".tfplan.json", policyPath, offline)
	wantRe := regexp.MustCompile(want)
	if want != "" && !wantRe.Match(got) {
		t.Fatalf("binary did not return expect output, \ngot=%s \nwant (regex)=%s", string(got), want)
	}
}

func terraform(t *testing.T, dir, name string) {
	terraformInit(t, "terraform", dir)
	terraformPlan(t, "terraform", dir, name+".tfplan")
	payload := terraformShow(t, "terraform", dir, name+".tfplan")
	saveFile(t, dir, name+".tfplan.json", payload)
}

func terraformInit(t *testing.T, executable, dir string) {
	terraformExec(t, executable, dir, "init", "-input=false")
}

func terraformPlan(t *testing.T, executable, dir, tfplan string) {
	terraformExec(t, executable, dir, "plan", "-input=false", "-refresh=false", "-out", tfplan)
}

func terraformShow(t *testing.T, executable, dir, tfplan string) []byte {
	return terraformExec(t, executable, dir, "show", "--json", tfplan)
}

func terraformExec(t *testing.T, executable, dir string, args ...string) []byte {
	cmd := exec.Command(executable, args...)
	cmd.Env = []string{"HOME=" + filepath.Join(dir, "fakehome")}
	cmd.Dir = dir
	wantError := false
	payload, _ := run(t, cmd, wantError)
	return payload
}

func saveFile(t *testing.T, dir, filename string, payload []byte) {
	fullpath := filepath.Join(dir, filename)
	f, err := os.Create(fullpath)
	if err != nil {
		t.Fatalf("error while creating file %s, error %v", fullpath, err)
	}
	_, err = f.Write(payload)
	if err != nil {
		t.Fatalf("error while writing to file %s, error %v", fullpath, err)
	}
}

func tfvConvert(t *testing.T, dir, tfplan string, offline bool) []byte {
	executable := tfvBinary
	wantError := false
	args := []string{"convert", "--project", data.Provider["project"]}
	if offline {
		args = append(args, "--offline", "--ancestry", data.Ancestry)
	}
	args = append(args, tfplan)
	cmd := exec.Command(executable, args...)
	// Remove environment variables inherited from the test runtime.
	cmd.Env = []string{}
	// Add credentials back.
	if data.Provider["credentials"] != "" {
		cmd.Env = append(cmd.Env, "GOOGLE_APPLICATION_CREDENTIALS="+data.Provider["credentials"])
	}
	cmd.Dir = dir
	payload, _ := run(t, cmd, wantError)
	return payload
}

func tfvValidate(t *testing.T, wantError bool, dir, tfplan, policyPath string, offline bool) []byte {
	executable := tfvBinary
	args := []string{"validate", "--project", data.Provider["project"], "--policy-path", policyPath}
	if offline {
		args = append(args, "--offline", "--ancestry", data.Ancestry)
	}
	args = append(args, tfplan)
	cmd := exec.Command(executable, args...)
	cmd.Env = []string{"GOOGLE_APPLICATION_CREDENTIALS=" + data.Provider["credentials"]}
	cmd.Dir = dir
	payload, _ := run(t, cmd, wantError)
	return payload
}

// run a command and call t.Fatal on non-zero exit.
func run(t *testing.T, cmd *exec.Cmd, wantError bool) ([]byte, []byte) {
	var stderr, stdout bytes.Buffer
	cmd.Stderr, cmd.Stdout = &stderr, &stdout
	err := cmd.Run()
	if gotError := (err != nil); gotError != wantError {
		t.Fatalf("running %s: \nerror=%v \nstderr=%s \nstdout=%s", cmdToString(cmd), err, stderr.String(), stdout.String())
	}
	// Print env, stdout and stderr if verbose flag is used.
	if len(cmd.Env) != 0 {
		t.Logf("=== Environment Variable of %s ===", cmdToString(cmd))
		t.Log(strings.Join(cmd.Env, "\n"))
	}
	if stdout.String() != "" {
		t.Logf("=== STDOUT of %s ===", cmdToString(cmd))
		t.Log(stdout.String())
	}
	if stderr.String() != "" {
		t.Logf("=== STDERR of %s ===", cmdToString(cmd))
		t.Log(stderr.String())
	}
	return stdout.Bytes(), stderr.Bytes()
}

// cmdToString clones the logic of https://golang.org/pkg/os/exec/#Cmd.String.
func cmdToString(c *exec.Cmd) string {
	// report the exact executable path (plus args)
	b := new(strings.Builder)
	b.WriteString(c.Path)
	for _, a := range c.Args[1:] {
		b.WriteByte(' ')
		b.WriteString(a)
	}
	return b.String()
}
