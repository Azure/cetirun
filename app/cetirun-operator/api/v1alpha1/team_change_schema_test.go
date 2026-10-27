// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	"os"
	"path/filepath"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestTeamChangeModelAccessSchemaForbidsBudgetOverride(t *testing.T) {
	for _, directory := range []string{"config/crd/bases", "chart/templates/crds"} {
		t.Run(directory, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", directory, "cetirun.microsoft.com_agents.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var definition apiextensionsv1.CustomResourceDefinition
			if err := yaml.UnmarshalStrict(data, &definition); err != nil {
				t.Fatal(err)
			}
			access := definition.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["teamChangeRequest"].Properties["modelAccess"]
			if len(access.XValidations) != 1 || access.XValidations[0].Rule != "!has(self.monthlyBudgetUSD)" {
				t.Fatalf("Team change modelAccess budget rule = %+v, want monthlyBudgetUSD forbidden", access.XValidations)
			}
		})
	}
}
