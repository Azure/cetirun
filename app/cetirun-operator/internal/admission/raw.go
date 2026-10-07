// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"reflect"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Typed comparisons omit future fields. Cleanup may change only finalizers and
// server-managed field ownership, even when compatible decoding is enabled.
func rawMetadataOnly(request admission.Request) bool {
	return rawChangesOnly(request, nil, []string{"finalizers", "managedFields"})
}

func rawChangesOnly(request admission.Request, specFields, metadataFields []string) bool {
	objects := [2]map[string]any{}
	for i, raw := range [][]byte{request.Object.Raw, request.OldObject.Raw} {
		if len(raw) == 0 || decodeJSON(raw, &objects[i], false) != nil || objects[i] == nil {
			return false
		}
		metadata, ok := objects[i]["metadata"].(map[string]any)
		if !ok {
			return false
		}
		for _, field := range metadataFields {
			delete(metadata, field)
		}
		if len(specFields) != 0 {
			spec, ok := objects[i]["spec"].(map[string]any)
			if !ok {
				return false
			}
			for _, field := range specFields {
				delete(spec, field)
			}
		}
	}
	return reflect.DeepEqual(objects[0], objects[1])
}

func governanceActionMetadataOnly(request admission.Request, annotations []string) bool {
	objects, ok := governanceActionRawObjects(request)
	if !ok {
		return false
	}
	for i := range objects {
		metadata, _ := objects[i]["metadata"].(map[string]any)
		values, _ := metadata["annotations"].(map[string]any)
		for _, key := range annotations {
			delete(values, key)
		}
		if len(values) == 0 {
			delete(metadata, "annotations")
		}
	}
	return reflect.DeepEqual(objects[0], objects[1])
}

func governanceActionSuspensionOnly(request admission.Request) bool {
	objects, ok := governanceActionRawObjects(request)
	if !ok {
		return false
	}
	for i := range objects {
		spec, ok := objects[i]["spec"].(map[string]any)
		if !ok {
			return false
		}
		delete(spec, "suspended")
	}
	return reflect.DeepEqual(objects[0], objects[1])
}

func governanceActionRawObjects(request admission.Request) ([2]map[string]any, bool) {
	objects := [2]map[string]any{}
	for i, raw := range [][]byte{request.Object.Raw, request.OldObject.Raw} {
		if len(raw) == 0 || decodeJSON(raw, &objects[i], false) != nil || objects[i] == nil {
			return objects, false
		}
		delete(objects[i], "status")
		metadata, ok := objects[i]["metadata"].(map[string]any)
		if !ok {
			return objects, false
		}
		for _, key := range []string{"resourceVersion", "generation", "managedFields"} {
			delete(metadata, key)
		}
	}
	return objects, true
}

// Unknown top-level spec changes must not inherit legacy Agent/Team authority.
// Unchanged unknown fields and new status fields do not block ordinary writes.
func unknownSpecChanged(request admission.Request, object client.Object) bool {
	var specType reflect.Type
	switch object.(type) {
	case *api.Agent:
		specType = reflect.TypeOf(api.AgentSpec{})
	case *api.Team:
		specType = reflect.TypeOf(api.TeamSpec{})
	default:
		return false // Sandbox and AgentPolicy writes already require an administrator.
	}
	unknown := [2]map[string]any{{}, {}}
	for i, raw := range [][]byte{request.Object.Raw, request.OldObject.Raw} {
		if len(raw) == 0 {
			continue
		}
		var decoded struct {
			Spec map[string]any `json:"spec"`
		}
		if decodeJSON(raw, &decoded, false) != nil {
			return true
		}
		for key, value := range decoded.Spec {
			unknown[i][key] = value
		}
		for field := range specType.NumField() {
			key, _, _ := strings.Cut(specType.Field(field).Tag.Get("json"), ",")
			delete(unknown[i], key)
		}
	}
	return !reflect.DeepEqual(unknown[0], unknown[1])
}
