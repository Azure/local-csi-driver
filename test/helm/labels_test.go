// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

func TestCommonLabels(t *testing.T) {
	tests := []struct {
		name   string
		values string
		labels map[string]string
	}{
		{name: "omitted"},
		{name: "empty", values: "commonLabels: {}"},
		{name: "null", values: "commonLabels: null"},
		{
			name: "custom labels",
			values: `commonLabels:
  example.com/owner: platform
  example.com/enabled: "true"
  example.com/number: "123"
  example.com/empty: ""
`,
			labels: map[string]string{
				"example.com/owner":   "platform",
				"example.com/enabled": "true",
				"example.com/number":  "123",
				"example.com/empty":   "",
			},
		},
		{
			name: "reserved labels",
			values: `commonLabels:
  app: override
  app.kubernetes.io/component: override
  app.kubernetes.io/name: override
  app.kubernetes.io/instance: override
  app.kubernetes.io/version: override
  app.kubernetes.io/managed-by: override
  helm.sh/chart: override
  example.com/owner: platform
`,
			labels: map[string]string{"example.com/owner": "platform"},
		},
	}

	for _, overrides := range []struct {
		name   string
		values string
	}{
		{name: "default selectors"},
		{
			name: "custom pod labels and selectors",
			values: `daemonset:
  podSelector:
    example.com/driver: custom
manager:
  deployment:
    podLabels:
      example.com/owner: pod-owner
`,
		},
	} {
		t.Run(overrides.name, func(t *testing.T) {
			baseline := render(t, overrides.values)
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					actual := render(t, overrides.values+"\n"+tt.values)
					if len(actual) != len(baseline) {
						t.Fatalf("resource count changed: got %d, want %d", len(actual), len(baseline))
					}
					for i, original := range baseline {
						var metadata metav1.PartialObjectMetadata
						if err := json.Unmarshal(actual[i], &metadata); err != nil {
							t.Fatalf("decode metadata: %v", err)
						}
						var expected map[string]any
						if err := json.Unmarshal(original, &expected); err != nil {
							t.Fatalf("decode baseline: %v", err)
						}
						labels := expected["metadata"].(map[string]any)["labels"].(map[string]any)
						for key, value := range tt.labels {
							labels[key] = value
							if metadata.Labels[key] != value {
								t.Errorf("%s %s: label %s = %q, want %q",
									metadata.Kind, metadata.Name, key, metadata.Labels[key], value)
							}
						}
						var got map[string]any
						if err := json.Unmarshal(actual[i], &got); err != nil {
							t.Fatalf("decode rendered resource: %v", err)
						}
						// Webhook metadata contributes to the existing manager restart checksums.
						for _, resource := range []map[string]any{got, expected} {
							unstructured.RemoveNestedField(resource, "spec", "template", "metadata", "annotations", "checksum/config")
							unstructured.RemoveNestedField(resource, "spec", "template", "metadata", "annotations", "checksum/config2")
						}
						if !reflect.DeepEqual(got, expected) {
							t.Errorf("%s %s: fields other than metadata labels and restart checksums changed",
								metadata.Kind, metadata.Name)
						}
					}
				})
			}
		})
	}
}

func render(t *testing.T, values string) []json.RawMessage {
	t.Helper()
	helm := os.Getenv("HELM")
	if helm == "" {
		helm = "helm"
	}
	cmd := exec.Command(helm, "template", "labels-test", "../../charts/latest", "--namespace", "test", "--values", "-")
	cmd.Stdin = strings.NewReader(values)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed (run make test-helm to install Helm): %v\n%s", err, output)
	}
	reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(output)))
	var resources []json.RawMessage
	for {
		document, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read YAML document: %v", err)
		}
		data, err := sigsyaml.YAMLToJSONStrict(document)
		if err != nil {
			t.Fatalf("rendered YAML is invalid or has duplicate keys: %v", err)
		}
		resources = append(resources, data)
	}
	if len(resources) == 0 {
		t.Fatal("helm template produced no resources")
	}
	return resources
}
