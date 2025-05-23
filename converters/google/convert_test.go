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

package google

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	converter "github.com/GoogleCloudPlatform/terraform-google-conversion/google"
	"github.com/GoogleCloudPlatform/terraform-validator/ancestrymanager"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

const testProject = "test-project"

func newTestConverter() (*Converter, error) {
	ctx := context.Background()
	ancestry := ""
	project := testProject
	offline := true
	ancestryManager, err := ancestrymanager.New(context.Background(), project, ancestry, offline)
	if err != nil {
		return nil, errors.Wrap(err, "constructing resource manager client")
	}
	c, err := NewConverter(ctx, ancestryManager, project, offline)
	if err != nil {
		return nil, errors.Wrap(err, "building converter")
	}
	return c, nil
}

type configAttrGetter func(cfg *converter.Config) string

func getCredentials(cfg *converter.Config) string {
	return cfg.Credentials
}
func getAccessToken(cfg *converter.Config) string {
	return cfg.AccessToken
}

func TestNewConverterCredentials(t *testing.T) {
	cases := []struct {
		name           string
		envKey         string
		envValue       string
		getConfigValue configAttrGetter
	}{
		{
			name:           "GOOGLE_CREDENTIALS",
			envKey:         "GOOGLE_CREDENTIALS",
			envValue:       "whatever",
			getConfigValue: getCredentials,
		},
		{
			name:           "GOOGLE_CLOUD_KEYFILE_JSON",
			envKey:         "GOOGLE_CLOUD_KEYFILE_JSON",
			envValue:       "whatever",
			getConfigValue: getCredentials,
		},
		{
			name:           "GCLOUD_KEYFILE_JSON",
			envKey:         "GCLOUD_KEYFILE_JSON",
			envValue:       "whatever",
			getConfigValue: getCredentials,
		},
		{
			name:           "GOOGLE_OAUTH_ACCESS_TOKEN",
			envKey:         "GOOGLE_OAUTH_ACCESS_TOKEN",
			envValue:       "whatever",
			getConfigValue: getAccessToken,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			originalValue, isSet := os.LookupEnv(c.envKey)
			err := os.Setenv(c.envKey, c.envValue)
			if err != nil {
				t.Fatalf("error setting env var %s=%s: %s", c.envKey, c.envValue, err)
			}

			converter, err := newTestConverter()
			if err != nil {
				t.Fatalf("error building converter: %s", err)
			}

			assert.EqualValues(t, c.getConfigValue(converter.cfg), c.envValue)

			if isSet {
				err = os.Setenv(c.envKey, originalValue)
				if err != nil {
					t.Fatalf("error setting env var %s=%s: %s", c.envKey, originalValue, err)
				}
			} else {
				err = os.Unsetenv(c.envKey)
				if err != nil {
					t.Fatalf("error unsetting env var %s: %s", c.envKey, err)
				}
			}
		})
	}
}

func TestSortByName(t *testing.T) {
	cases := []struct {
		name           string
		unsorted       []Asset
		expectedSorted []Asset
	}{
		{
			name:           "Empty",
			unsorted:       []Asset{},
			expectedSorted: []Asset{},
		},
		{
			name:           "BCAtoABC",
			unsorted:       []Asset{{Name: "b", Type: "b-type"}, {Name: "c", Type: "c-type"}, {Name: "a", Type: "a-type"}},
			expectedSorted: []Asset{{Name: "a", Type: "a-type"}, {Name: "b", Type: "b-type"}, {Name: "c", Type: "c-type"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assets := c.unsorted
			sort.Sort(byName(assets))
			assert.EqualValues(t, c.expectedSorted, assets)
		})
	}
}

func TestAddResourceChanges_unknownResourceIgnored(t *testing.T) {
	rc := tfjson.ResourceChange{
		Address:      "whatever.google_unknown.foo",
		Mode:         "managed",
		Type:         "google_unknown",
		Name:         "foo",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"change"},
			Before:  nil,
			After:   nil,
		},
	}
	c, err := newTestConverter()
	assert.Nil(t, err)
	err = c.AddResourceChanges([]*tfjson.ResourceChange{&rc})
	assert.Nil(t, err)
	assert.EqualValues(t, map[string]Asset{}, c.assets)
}

func TestAddResourceChanges_unsupportedResourceIgnored(t *testing.T) {
	rc := tfjson.ResourceChange{
		Address:      "whatever.google_unknown.foo",
		Mode:         "managed",
		Type:         "google_unsupported",
		Name:         "foo",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"change"},
			Before:  nil,
			After:   nil,
		},
	}
	c, err := newTestConverter()
	assert.Nil(t, err)

	// fake that this resource is known to the provider; it will never be "supported" by the
	// converter.
	c.schema.ResourcesMap[rc.Type] = c.schema.ResourcesMap["google_compute_disk"]

	err = c.AddResourceChanges([]*tfjson.ResourceChange{&rc})
	assert.Nil(t, err)
	assert.EqualValues(t, map[string]Asset{}, c.assets)
}

func TestAddResourceChanges_noopIgnored(t *testing.T) {
	rc := tfjson.ResourceChange{
		Address:      "whatever.google_compute_disk.foo",
		Mode:         "managed",
		Type:         "google_compute_disk",
		Name:         "foo",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"no-op"},
			Before:  nil,
			After:   nil,
		},
	}
	c, err := newTestConverter()
	assert.Nil(t, err)

	err = c.AddResourceChanges([]*tfjson.ResourceChange{&rc})
	assert.Nil(t, err)
	assert.EqualValues(t, map[string]Asset{}, c.assets)
}

func TestAddResourceChanges_deleteProcessed(t *testing.T) {
	rc := tfjson.ResourceChange{
		Address:      "whatever.google_compute_disk.foo",
		Mode:         "managed",
		Type:         "google_compute_disk",
		Name:         "foo",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"delete"},
			Before: map[string]interface{}{
				"project": testProject,
				"name":    "test-disk",
				"type":    "pd-ssd",
				"zone":    "us-central1-a",
				"image":   "projects/debian-cloud/global/images/debian-8-jessie-v20170523",
				"labels": map[string]interface{}{
					"environment": "dev",
				},
				"physical_block_size_bytes": 4096,
			},
			After: nil,
		},
	}
	c, err := newTestConverter()
	assert.Nil(t, err)

	err = c.AddResourceChanges([]*tfjson.ResourceChange{&rc})
	assert.Nil(t, err)
	assert.EqualValues(t, map[string]Asset{}, c.assets)
}

func TestAddResourceChanges_createOrUpdateOrDeleteCreateProcessed(t *testing.T) {
	cases := []struct {
		name    string
		actions tfjson.Actions
	}{
		{
			name:    "Create",
			actions: tfjson.Actions{"create"},
		},
		{
			name:    "Update",
			actions: tfjson.Actions{"update"},
		},
		{
			name:    "DeleteCreate",
			actions: tfjson.Actions{"delete", "create"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := tfjson.ResourceChange{
				Address:      "whatever.google_compute_disk.foo",
				Mode:         "managed",
				Type:         "google_compute_disk",
				Name:         "foo",
				ProviderName: "google",
				Change: &tfjson.Change{
					Actions: c.actions,
					Before:  nil, // Ignore Before because it's unused
					After: map[string]interface{}{
						"project": testProject,
						"name":    "test-disk",
						"type":    "pd-ssd",
						"zone":    "us-central1-a",
						"image":   "projects/debian-cloud/global/images/debian-8-jessie-v20170523",
						"labels": map[string]interface{}{
							"environment": "dev",
						},
						"physical_block_size_bytes": 4096,
					},
				},
			}
			c, err := newTestConverter()
			assert.Nil(t, err)

			err = c.AddResourceChanges([]*tfjson.ResourceChange{&rc})
			assert.Nil(t, err)

			caiKey := "compute.googleapis.com/Disk//compute.googleapis.com/projects/test-project/zones/us-central1-a/disks/test-disk"
			assert.Contains(t, c.assets, caiKey)
		})
	}
}

func TestAddDuplicatedResources(t *testing.T) {
	rcb1 := tfjson.ResourceChange{
		Address:      "google_billing_budget.budget1",
		Mode:         "managed",
		Type:         "google_billing_budget",
		Name:         "budget1",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"create"},
			Before:  nil,
			After: map[string]interface{}{
				"all_updates_rule": []map[string]interface{}{},
				"amount": []map[string]interface{}{
					{
						"last_period_amount": nil,
						"specified_amount": []map[string]interface{}{
							{
								"currency_code": "USD",
								"nanos":         nil,
								"units":         "100",
							},
						},
					},
				},
				"billing_account": "000000-000000-000000",
				"budget_filter": []map[string]interface{}{
					{
						"credit_types_treatment": "INCLUDE_ALL_CREDITS",
					},
				},
				"display_name": "Example Billing Budget 1",
				"threshold_rules": []map[string]interface{}{
					{
						"spend_basis":       "CURRENT_SPEND",
						"threshold_percent": 0.5,
					},
				},
				"timeouts": nil,
			},
		},
	}
	rcb2 := tfjson.ResourceChange{
		Address:      "google_billing_budget.budget2",
		Mode:         "managed",
		Type:         "google_billing_budget",
		Name:         "budget2",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"create"},
			Before:  nil,
			After: map[string]interface{}{
				"all_updates_rule": []map[string]interface{}{},
				"amount": []map[string]interface{}{
					{
						"last_period_amount": nil,
						"specified_amount": []map[string]interface{}{
							{
								"currency_code": "USD",
								"nanos":         nil,
								"units":         "100",
							},
						},
					},
				},
				"billing_account": "000000-000000-000000",
				"budget_filter": []map[string]interface{}{
					{
						"credit_types_treatment": "INCLUDE_ALL_CREDITS",
					},
				},
				"display_name": "Example Billing Budget 2",
				"threshold_rules": []map[string]interface{}{
					{
						"spend_basis":       "CURRENT_SPEND",
						"threshold_percent": 0.5,
					},
				},
				"timeouts": nil,
			},
		},
	}
	rcp1 := tfjson.ResourceChange{
		Address:      "google_project.my_project1",
		Mode:         "managed",
		Type:         "google_project",
		Name:         "my_project1",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"create"},
			Before:  nil,
			After: map[string]interface{}{
				"auto_create_network": true,
				"billing_account":     "000000-000000-000000",
				"labels":              nil,
				"name":                "My Project1",
				"org_id":              "00000000000000",
				"timeouts":            nil,
			},
		},
	}
	rcp2 := tfjson.ResourceChange{
		Address:      "google_project.my_project2",
		Mode:         "managed",
		Type:         "google_project",
		Name:         "my_project2",
		ProviderName: "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"create"},
			Before:  nil,
			After: map[string]interface{}{
				"auto_create_network": true,
				"billing_account":     "000000-000000-000000",
				"labels":              nil,
				"name":                "My Project2",
				"org_id":              "00000000000000",
				"timeouts":            nil,
			},
		},
	}
	c, err := newTestConverter()
	assert.Nil(t, err)

	err = c.AddResourceChanges([]*tfjson.ResourceChange{&rcb1, &rcb2, &rcp1, &rcp2})
	assert.Nil(t, err)

	caiKeyBilling := "cloudbilling.googleapis.com/ProjectBillingInfo//cloudbilling.googleapis.com/projects/test-project/billingInfo"
	assert.Contains(t, c.assets, caiKeyBilling)

	caiKeyProject := "cloudresourcemanager.googleapis.com/Project//cloudresourcemanager.googleapis.com/projects/test-project"
	assert.Contains(t, c.assets, caiKeyProject)
}

func TestAddStorageModuleAfterUnknown(t *testing.T) {
	var nilValue map[string]interface{} = nil
	rc := tfjson.ResourceChange{
		Address:       "module.gcs_buckets.google_storage_bucket.buckets[0]",
		ModuleAddress: "module.gcs_buckets",
		Mode:          "managed",
		Type:          "google_storage_bucket",
		Name:          "buckets",
		Index:         0,
		ProviderName:  "google",
		Change: &tfjson.Change{
			Actions: tfjson.Actions{"create"},
			Before:  nil,
			After: map[string]interface{}{
				"cors": []interface{}{
					nilValue,
				},
				"default_event_based_hold": nil,
				"encryption": []interface{}{
					nilValue,
				},
				"lifecycle_rule":   []interface{}{},
				"location":         "US",
				"logging":          []interface{}{},
				"project":          "test-project",
				"requester_pays":   nil,
				"retention_policy": []interface{}{},
				"storage_class":    "MULTI_REGIONAL",
				"versioning": []interface{}{
					nilValue,
				},
				"website": []interface{}{
					nilValue,
				},
			},
		},
	}
	c, err := newTestConverter()
	assert.Nil(t, err)

	err = c.AddResourceChanges([]*tfjson.ResourceChange{&rc})
	assert.Nil(t, err)
	assert.Len(t, c.assets, 1)
	for key := range c.assets {
		assert.EqualValues(t, c.assets[key].Type, "storage.googleapis.com/Bucket")
	}

}

func TestTimestampMarshalJSON(t *testing.T) {
	expectedJSON := []byte("\"2021-04-14T15:16:17Z\"")
	date := time.Date(2021, time.April, 14, 15, 16, 17, 0, time.UTC)
	ts := Timestamp{
		Seconds: int64(date.Unix()),
		Nanos:   int64(date.UnixNano()),
	}
	json, err := ts.MarshalJSON()
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}
	assert.EqualValues(t, json, expectedJSON)
}

func TestTimestampUnmarshalJSON(t *testing.T) {
	expectedDate := time.Date(2021, time.April, 14, 15, 16, 17, 0, time.UTC)
	expected := Timestamp{
		Seconds: int64(expectedDate.Unix()),
		Nanos:   int64(expectedDate.UnixNano()),
	}
	json := []byte("\"2021-04-14T15:16:17Z\"")
	ts := Timestamp{}
	err := ts.UnmarshalJSON(json)
	if err != nil {
		t.Fatalf("Unexpected error: %s", err)
	}
	assert.EqualValues(t, ts, expected)
}
