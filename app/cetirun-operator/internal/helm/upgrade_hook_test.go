// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// upgradeHookObjects renders the agent chart for an upgrade and returns the
// objects of its pre-upgrade hook by kind.
func upgradeHookObjects(t *testing.T, values map[string]any) map[string]map[string]any {
	t.Helper()
	chrt := testChart(t)
	prepared, err := PrepareChart(chrt, values)
	if err != nil {
		t.Fatal(err)
	}
	renderValues, err := chartutil.ToRenderValues(chrt, prepared.Values,
		chartutil.ReleaseOptions{Name: "cetirun-example", Namespace: "cetirun-example", Revision: 2, IsUpgrade: true},
		chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(chrt, renderValues)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]map[string]any{}
	for _, doc := range strings.Split(rendered["cetirun/templates/pre-upgrade-hook.yaml"], "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		if kind, _ := obj["kind"].(string); kind != "" {
			objects[kind] = obj
		}
	}
	if objects["Job"] == nil {
		t.Fatal("the agent chart renders no pre-upgrade hook Job")
	}
	return objects
}

func upgradeHookJob(t *testing.T, values map[string]any) map[string]any {
	t.Helper()
	return upgradeHookObjects(t, values)["Job"]
}

// upgradeHookScript returns the shell script of the hook Job.
func upgradeHookScript(t *testing.T, job map[string]any) string {
	t.Helper()
	container := object(array(object(object(object(job["spec"])["template"])["spec"])["containers"])[0])
	command := array(container["command"])
	script, _ := command[len(command)-1].(string)
	if script == "" {
		t.Fatal("the hook Job runs no script")
	}
	return script
}

func upgradeHookInput(typ string) map[string]any {
	input := sampleInput(typ, "openai", false)
	input["persistence"] = map[string]any{"size": "120Gi"}
	return input
}

func TestUpgradeHookHasAUsableHome(t *testing.T) {
	for _, typ := range []string{"hermes", "openclaw"} {
		t.Run(typ, func(t *testing.T) {
			spec := object(object(object(upgradeHookJob(t, upgradeHookInput(typ))["spec"])["template"])["spec"])
			containers := array(spec["containers"])
			if len(containers) != 1 {
				t.Fatalf("hook containers: %v", containers)
			}
			container := object(containers[0])
			if object(container["securityContext"])["readOnlyRootFilesystem"] != true {
				t.Fatal("the hook must keep a read-only root filesystem")
			}
			home := ""
			for _, item := range array(container["env"]) {
				if object(item)["name"] == "HOME" {
					home, _ = object(item)["value"].(string)
				}
			}
			if home == "" {
				t.Fatal("the hook does not set HOME; the image user's home directory is /dev/null")
			}
			mounted := ""
			for _, item := range array(container["volumeMounts"]) {
				if object(item)["mountPath"] == home {
					mounted, _ = object(item)["name"].(string)
				}
			}
			writable := false
			for _, item := range array(spec["volumes"]) {
				volume := object(item)
				_, emptyDir := volume["emptyDir"]
				writable = writable || (mounted != "" && volume["name"] == mounted && emptyDir)
			}
			if !writable {
				t.Fatalf("HOME %q of the hook is not a writable emptyDir", home)
			}
		})
	}
}

func TestUpgradeHookCanWaitForTheDeletion(t *testing.T) {
	objects := upgradeHookObjects(t, upgradeHookInput("hermes"))
	job := objects["Job"]
	script := upgradeHookScript(t, job)
	name := regexp.MustCompile(`STS_NAME="([^"]+)"`).FindStringSubmatch(script)
	if name == nil {
		t.Fatal("the hook script names no StatefulSet")
	}
	// kubectl delete waits with a list and a watch that select this name.
	rules := array(object(objects["Role"])["rules"])
	if len(rules) != 1 {
		t.Fatalf("hook Role rules: %v", rules)
	}
	rule := object(rules[0])
	verbs := map[string]bool{}
	for _, verb := range array(rule["verbs"]) {
		text, _ := verb.(string)
		verbs[text] = true
	}
	if !reflect.DeepEqual(array(rule["resourceNames"]), []any{name[1]}) ||
		len(verbs) != 4 || !verbs["get"] || !verbs["list"] || !verbs["watch"] || !verbs["delete"] {
		t.Fatalf("the hook Role must allow get, list, watch, and delete of %s only: %v", name[1], rule)
	}
	deadline, _ := object(job["spec"])["activeDeadlineSeconds"].(float64)
	wait := regexp.MustCompile(`kubectl delete statefulset .*--timeout=(\d+)s`).FindStringSubmatch(script)
	if wait == nil {
		t.Fatal("the orphan delete has no timeout")
	}
	seconds, _ := strconv.Atoi(wait[1])
	if seconds <= 0 || float64(seconds) >= deadline {
		t.Fatalf("delete timeout %ds must be below the Job deadline %vs", seconds, deadline)
	}
}

// The jsonpath queries of the hook and the value of each for a StatefulSet that
// matches the rendered expectations.
var upgradeHookQueries = map[string]string{
	`{.spec.serviceName}`: "SERVICE_NAME",
	`{.spec.selector.matchLabels.app\.kubernetes\.io/name}`:           "SELECTOR_NAME",
	`{.spec.selector.matchLabels.app\.kubernetes\.io/instance}`:       "SELECTOR_INSTANCE",
	`{.spec.podManagementPolicy}`:                                     "",
	`{.spec.volumeClaimTemplates[*].metadata.name}`:                   "VCT_NAMES",
	`{.spec.volumeClaimTemplates[0].spec.storageClassName}`:           "VCT_STORAGE_CLASS",
	`{.spec.volumeClaimTemplates[0].spec.accessModes[0]}`:             "VCT_ACCESS_MODE",
	`{.spec.volumeClaimTemplates[0].spec.resources.requests.storage}`: "VCT_SIZE",
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// writeKubectlStub writes a kubectl that logs its arguments, answers the hook's
// existence check and jsonpath queries from fields, and fails as requested.
func writeKubectlStub(t *testing.T, dir string, fields map[string]string, getError, missing, failQuery string) {
	t.Helper()
	var arms []string
	queries := make([]string, 0, len(fields))
	for query := range fields {
		queries = append(queries, query)
	}
	sort.Strings(queries)
	for _, query := range queries {
		answer := "printf '%s' " + shellQuote(fields[query])
		if query == failQuery {
			answer = "printf 'error: the server was unable to return a response\\n' >&2; exit 1"
		}
		arms = append(arms, fmt.Sprintf("\t\t%s) %s ;;", shellQuote("jsonpath="+query), answer))
	}
	stub := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$STUB_DIR/calls"`,
		`if [ "$1" = delete ]; then exit 0; fi`,
		`case "$*" in`,
		`*--ignore-not-found*)`,
		fmt.Sprintf("\tif [ -n %s ]; then printf '%%s\\n' %s >&2; exit 1; fi", shellQuote(getError), shellQuote(getError)),
		fmt.Sprintf("\tif [ -n %s ]; then exit 0; fi", shellQuote(missing)),
		`	printf 'statefulset.apps/%s\n' "$3"`,
		`	exit 0 ;;`,
		`esac`,
		`for arg in "$@"; do`,
		`	case "$arg" in`,
		strings.Join(arms, "\n"),
		`	jsonpath=*) printf 'unexpected query %s\n' "$arg" >&2; exit 3 ;;`,
		`	esac`,
		`done`,
		`exit 0`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The query of the hook for the size of the HOME claim template.
const upgradeHookSizeQuery = `{.spec.volumeClaimTemplates[0].spec.resources.requests.storage}`

func TestUpgradeHookScript(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("requires a POSIX sh")
	}
	for _, tc := range []struct {
		// size is the rendered persistence.size, and current the size of the
		// StatefulSet; both default to 120Gi.
		name, size, current, getError, missing, failQuery string
		succeeds, deletes                                 bool
		output                                            string
	}{
		// The image user's HOME made every kubectl call fail like this; the hook
		// must not treat it as a missing StatefulSet.
		{name: "kubectl error", getError: "error: stat /dev/null/.kube/config: not a directory", output: "not a directory"},
		{name: "missing StatefulSet", missing: "yes", succeeds: true, output: "not found, nothing to do"},
		{name: "unchanged", succeeds: true, output: "No immutable field changes detected"},
		// Kubernetes keeps 1024Gi as 1Ti.
		{name: "same size in canonical form", size: "1024Gi", current: "1Ti", succeeds: true,
			output: "No immutable field changes detected"},
		{name: "grown volume", current: "100Gi", succeeds: true, deletes: true,
			output: "volumeClaimTemplate size: '100Gi' -> '120Gi'"},
		{name: "grown volume to a size in another form", size: "1024Gi", current: "1000Gi", succeeds: true, deletes: true,
			output: "volumeClaimTemplate size: '1000Gi' -> '1024Gi'"},
		{name: "failed read", current: "100Gi", failQuery: `{.spec.serviceName}`, output: "unable to return a response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := upgradeHookInput("hermes")
			if tc.size != "" {
				input["persistence"] = map[string]any{"size": tc.size}
			}
			script := upgradeHookScript(t, upgradeHookJob(t, input))
			expected := map[string]string{}
			for _, match := range regexp.MustCompile(`(?m)^\s*EXPECT_([A-Z_]+)="([^"]*)"$`).FindAllStringSubmatch(script, -1) {
				expected[match[1]] = match[2]
			}
			if expected["VCT_SIZE"] != object(input["persistence"])["size"] || expected["SERVICE_NAME"] == "" {
				t.Fatalf("unexpected rendered expectations: %v", expected)
			}
			fields := map[string]string{}
			for query, key := range upgradeHookQueries {
				fields[query] = expected[key]
				if key == "" {
					fields[query] = "OrderedReady"
				}
			}
			if tc.current != "" {
				fields[upgradeHookSizeQuery] = tc.current
			}
			dir := t.TempDir()
			writeKubectlStub(t, dir, fields, tc.getError, tc.missing, tc.failQuery)
			cmd := exec.Command(shell, "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "STUB_DIR="+dir)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.succeeds {
				t.Fatalf("exit error %v, want success %v; output:\n%s", err, tc.succeeds, out)
			}
			if !strings.Contains(string(out), tc.output) {
				t.Fatalf("output lacks %q:\n%s", tc.output, out)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			deleted := strings.Contains(string(calls), "delete statefulset "+expected["SERVICE_NAME"]+" -n cetirun-example --cascade=orphan")
			if deleted != tc.deletes {
				t.Fatalf("orphan delete %v, want %v; calls:\n%s", deleted, tc.deletes, calls)
			}
		})
	}
}

// The hook compares sizes in the canonical form in which Kubernetes keeps the
// StatefulSet. Otherwise a size such as 1024Gi, which Kubernetes keeps as 1Ti,
// would replace the StatefulSet object at each upgrade.
func TestUpgradeHookWritesSizesInCanonicalForm(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("requires a POSIX sh")
	}
	script := upgradeHookScript(t, upgradeHookJob(t, upgradeHookInput("hermes")))
	function := regexp.MustCompile(`(?ms)^canonical\(\) \{$.*?^\}$`).FindString(script)
	if function == "" {
		t.Fatal("the hook script defines no canonical function")
	}
	canonical := func(size string) string {
		out, err := exec.Command(shell, "-c", "set -e\n"+function+"\ncanonical \"$1\"", "sh", size).CombinedOutput()
		if err != nil {
			t.Fatalf("canonical %s: %v\n%s", size, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Whole numbers with or without a suffix: the form of Kubernetes.
	for _, size := range []string{
		"120Gi", "1024Gi", "2048Mi", "1536Mi", "1048576Ki", "1Ti", "1024Pi",
		"1000M", "1500M", "1000000k", "100", "1000", "128974848000",
	} {
		quantity := resource.MustParse(size)
		if got, want := canonical(size), quantity.String(); got != want {
			t.Errorf("canonical %s = %s, Kubernetes keeps %s", size, got, want)
		}
	}
	// Other forms stay as they are.
	for _, size := range []string{"1.5Gi", "0.5Ti", "1e3", "100m", "1Ei", "010Gi", "0", "1234567890123456789Ki"} {
		if got := canonical(size); got != size {
			t.Errorf("canonical %s = %s, want it unchanged", size, got)
		}
	}
}
