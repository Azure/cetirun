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
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// upgradeRender renders the agent chart for an upgrade of the release
// cetirun-example, by template file.
func upgradeRender(t *testing.T, values map[string]any) map[string]string {
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
	return rendered
}

// renderedObjects returns the objects of one rendered template by kind.
func renderedObjects(t *testing.T, manifest string) map[string]map[string]any {
	t.Helper()
	objects := map[string]map[string]any{}
	for _, doc := range strings.Split(manifest, "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		if kind, _ := obj["kind"].(string); kind != "" {
			objects[kind] = obj
		}
	}
	return objects
}

// upgradeHookObjects renders the agent chart for an upgrade and returns the
// objects of its pre-upgrade hook by kind.
func upgradeHookObjects(t *testing.T, values map[string]any) map[string]map[string]any {
	t.Helper()
	objects := renderedObjects(t, upgradeRender(t, values)["cetirun/templates/pre-upgrade-hook.yaml"])
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
	// kubectl delete waits with a list and a watch that select this name. The
	// hook also reads the StatefulSet's Pod, for where it keeps HOME.
	rules := array(object(objects["Role"])["rules"])
	if len(rules) != 2 {
		t.Fatalf("hook Role rules: %v", rules)
	}
	rule := object(rules[0])
	verbs := map[string]bool{}
	for _, verb := range array(rule["verbs"]) {
		text, _ := verb.(string)
		verbs[text] = true
	}
	if !reflect.DeepEqual(array(rule["resources"]), []any{"statefulsets"}) ||
		!reflect.DeepEqual(array(rule["resourceNames"]), []any{name[1]}) ||
		len(verbs) != 4 || !verbs["get"] || !verbs["list"] || !verbs["watch"] || !verbs["delete"] {
		t.Fatalf("the hook Role must allow get, list, watch, and delete of %s only: %v", name[1], rule)
	}
	pods := object(rules[1])
	if !reflect.DeepEqual(array(pods["apiGroups"]), []any{""}) || !reflect.DeepEqual(array(pods["resources"]), []any{"pods"}) ||
		!reflect.DeepEqual(array(pods["resourceNames"]), []any{name[1] + "-0"}) ||
		!reflect.DeepEqual(array(pods["verbs"]), []any{"get"}) {
		t.Fatalf("the hook Role must allow get of the Pod %s-0 only: %v", name[1], pods)
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

// The jsonpath queries of the hook.
const (
	upgradeHookServiceNameQuery  = `{.spec.serviceName}`
	upgradeHookSelectorNameQuery = `{.spec.selector.matchLabels.app\.kubernetes\.io/name}`
	upgradeHookInstanceQuery     = `{.spec.selector.matchLabels.app\.kubernetes\.io/instance}`
	upgradeHookPolicyQuery       = `{.spec.podManagementPolicy}`
	upgradeHookClaimNamesQuery   = `{.spec.volumeClaimTemplates[*].metadata.name}`
	upgradeHookClassQuery        = `{.spec.volumeClaimTemplates[0].spec.storageClassName}`
	upgradeHookAccessModeQuery   = `{.spec.volumeClaimTemplates[0].spec.accessModes[0]}`
	upgradeHookSizeQuery         = `{.spec.volumeClaimTemplates[0].spec.resources.requests.storage}`
	upgradeHookPodHomeQuery      = `{.metadata.name}/{.spec.volumes[?(@.name=="data")].persistentVolumeClaim.claimName}`
)

// upgradeStatefulSet returns the StatefulSet that the agent chart renders for
// an upgrade with values.
func upgradeStatefulSet(t *testing.T, values map[string]any) map[string]any {
	t.Helper()
	sts := renderedObjects(t, upgradeRender(t, values)["cetirun/templates/statefulset.yaml"])["StatefulSet"]
	if sts == nil {
		t.Fatal("the agent chart renders no StatefulSet")
	}
	return sts
}

// liveStatefulSetFields answers the hook's jsonpath queries for the StatefulSet
// that Kubernetes keeps when Helm applies sts: with the API's default
// podManagementPolicy, and the size in the canonical form of a quantity. It
// also answers for the StatefulSet's Pod, which mounts the claim of its claim
// template, or else the volume of its template.
func liveStatefulSetFields(t *testing.T, sts map[string]any) map[string]string {
	t.Helper()
	spec := object(sts["spec"])
	labels := object(object(spec["selector"])["matchLabels"])
	policy := text(spec["podManagementPolicy"])
	if policy == "" {
		policy = "OrderedReady"
	}
	fields := map[string]string{
		upgradeHookServiceNameQuery:  text(spec["serviceName"]),
		upgradeHookSelectorNameQuery: text(labels["app.kubernetes.io/name"]),
		upgradeHookInstanceQuery:     text(labels["app.kubernetes.io/instance"]),
		upgradeHookPolicyQuery:       policy,
	}
	var names []string
	templates := array(spec["volumeClaimTemplates"])
	for _, item := range templates {
		names = append(names, text(object(object(item)["metadata"])["name"]))
	}
	fields[upgradeHookClaimNamesQuery] = strings.Join(names, " ")
	if len(templates) > 0 {
		claim := object(object(templates[0])["spec"])
		fields[upgradeHookClassQuery] = text(claim["storageClassName"])
		fields[upgradeHookAccessModeQuery] = text(array(claim["accessModes"])[0])
		size, err := resource.ParseQuantity(text(object(object(claim["resources"])["requests"])["storage"]))
		if err != nil {
			t.Fatal(err)
		}
		fields[upgradeHookSizeQuery] = size.String()
	}
	pod := text(object(sts["metadata"])["name"]) + "-0"
	home := ""
	for _, item := range array(object(object(spec["template"])["spec"])["volumes"]) {
		if volume := object(item); volume["name"] == "data" {
			home = text(object(volume["persistentVolumeClaim"])["claimName"])
		}
	}
	if slices.Contains(names, "data") {
		home = "data-" + pod
	}
	fields[upgradeHookPodHomeQuery] = pod + "/" + home
	if fields[upgradeHookServiceNameQuery] == "" || fields[upgradeHookSelectorNameQuery] == "" {
		t.Fatalf("unexpected rendered StatefulSet: %v", fields)
	}
	return fields
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// writeKubectlStub writes a kubectl that logs its arguments, answers the hook's
// existence check of the StatefulSet and its jsonpath queries from fields, and
// fails as requested. It has only the Pod that the answer about the Pod's HOME
// names: a query for another Pod finds none.
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
	pod, _, _ := strings.Cut(fields[upgradeHookPodHomeQuery], "/")
	stub := strings.Join([]string{
		"#!/bin/sh",
		`printf '%s\n' "$*" >> "$STUB_DIR/calls"`,
		`if [ "$1" = delete ]; then exit 0; fi`,
		fmt.Sprintf(`if [ "$1" = get ] && [ "$2" = pod ] && [ "$3" != %s ]; then exit 0; fi`, shellQuote(pod)),
		`case "$*" in`,
		`*statefulset*--ignore-not-found*)`,
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

func TestUpgradeHookScript(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("requires a POSIX sh")
	}
	for _, tc := range []struct {
		// persistence is the rendered persistence values, with the size 120Gi
		// unless it says otherwise. The live StatefulSet is the one that the same
		// values render, as Kubernetes keeps it; live overrides its fields.
		name, getError, missing, failQuery string
		persistence                        map[string]any
		live                               map[string]string
		succeeds, deletes                  bool
		// output is in the hook's output, and absent is not.
		output, absent string
	}{
		// The image user's HOME made every kubectl call fail like this; the hook
		// must not treat it as a missing StatefulSet.
		{name: "kubectl error", getError: "error: stat /dev/null/.kube/config: not a directory", output: "not a directory"},
		{name: "missing StatefulSet", missing: "yes", succeeds: true, output: "not found, nothing to do"},
		{name: "unchanged", succeeds: true, output: "No immutable field changes detected"},
		{name: "unchanged with a storage class and another access mode",
			persistence: map[string]any{"storageClass": "managed-csi-premium", "accessMode": "ReadWriteOncePod"},
			succeeds:    true, output: "No immutable field changes detected"},
		{name: "unchanged with the cluster's default storage class", persistence: map[string]any{"storageClass": ""},
			succeeds: true, output: "No immutable field changes detected"},
		// Kubernetes keeps 1024Gi as 1Ti.
		{name: "same size in canonical form", persistence: map[string]any{"size": "1024Gi"}, succeeds: true,
			output: "No immutable field changes detected"},
		{name: "grown volume", live: map[string]string{upgradeHookSizeQuery: "100Gi"}, succeeds: true, deletes: true,
			output: "volumeClaimTemplate size: '100Gi' -> '120Gi'"},
		{name: "grown volume to a size in another form", persistence: map[string]any{"size": "1024Gi"},
			live: map[string]string{upgradeHookSizeQuery: "1000Gi"}, succeeds: true, deletes: true,
			output: "volumeClaimTemplate size: '1000Gi' -> '1024Gi'"},
		{name: "changed storage class", live: map[string]string{upgradeHookClassQuery: "other"}, succeeds: true,
			deletes: true, output: "volumeClaimTemplate storageClassName: 'other' -> 'default'"},
		{name: "changed access mode", live: map[string]string{upgradeHookAccessModeQuery: "ReadWriteOncePod"},
			succeeds: true, deletes: true, output: "volumeClaimTemplate accessMode: 'ReadWriteOncePod' -> 'ReadWriteOnce'"},
		// A StatefulSet with another selector could take over no orphaned Pod.
		{name: "changed selector", live: map[string]string{upgradeHookSelectorNameQuery: "other"},
			output: "selector app.kubernetes.io/name: 'other' -> 'cetirun'"},
		// Its recovery deletes only the StatefulSet object: a normal deletion would take the HOME claim with it
		// when persistence.retainPolicy is Delete. The whole command is on one line, so that it is copied whole.
		{name: "changed selector, recovery", live: map[string]string{upgradeHookSelectorNameQuery: "other"},
			output: "so that its HOME claim stays also with\npersistence.retainPolicy Delete:\n" +
				"kubectl delete statefulset cetirun-example -n cetirun-example --cascade=orphan\n" +
				"When it is gone, delete its Pod cetirun-example-0.\n"},
		{name: "changed selector, recovery keeps the claim of the StatefulSet",
			live:   map[string]string{upgradeHookSelectorNameQuery: "other"},
			output: "then mounts the same HOME claim, data-cetirun-example-0."},
		{name: "changed selector, recovery keeps an existing claim",
			persistence: map[string]any{"existingClaim": "home-example"},
			live:        map[string]string{upgradeHookSelectorNameQuery: "other"},
			output:      "then mounts the same HOME claim, home-example."},
		{name: "changed selector, recovery keeps the claim of the chart",
			persistence: map[string]any{"useStatefulSetVolumeClaim": false},
			live:        map[string]string{upgradeHookSelectorNameQuery: "other"},
			output:      "then mounts the same HOME claim, cetirun-example-data."},
		// An emptyDir HOME does not outlive the running Pod: only reverting the selector keeps it.
		{name: "changed selector, HOME in an emptyDir", persistence: map[string]any{"enabled": false},
			live: map[string]string{upgradeHookSelectorNameQuery: "other"},
			output: "Set nameOverride back.\nHOME of the running Pod is in an emptyDir.\n" +
				"HOME of a new Pod would be in an emptyDir, without the files of the running one.",
			absent: "--cascade=orphan"},
		// The Pod tells where it keeps HOME: a rollout to a claim may not have replaced it yet.
		{name: "changed selector, the running Pod still has an emptyDir HOME",
			live: map[string]string{upgradeHookSelectorNameQuery: "other", upgradeHookPodHomeQuery: "cetirun-example-0/"},
			output: "HOME of the running Pod is in an emptyDir.\n" +
				"HOME of a new Pod would be in the claim data-cetirun-example-0, without the files of the running one.",
			absent: "--cascade=orphan"},
		{name: "changed selector, no running Pod",
			live:   map[string]string{upgradeHookSelectorNameQuery: "other", upgradeHookPodHomeQuery: ""},
			output: "Set nameOverride back.\nNo Pod cetirun-example-0 runs, so the hook cannot tell where it kept HOME.",
			absent: "--cascade=orphan"},
		{name: "changed selector, HOME in another claim than the new one",
			persistence: map[string]any{"existingClaim": "home-example"},
			live: map[string]string{upgradeHookSelectorNameQuery: "other", upgradeHookClaimNamesQuery: "data",
				upgradeHookPodHomeQuery: "cetirun-example-0/data-cetirun-example-0"},
			output: "HOME of the running Pod is in the claim data-cetirun-example-0.\n" +
				"HOME of a new Pod would be in the claim home-example, without the files of the running one.",
			absent: "--cascade=orphan"},
		{name: "changed selector, HOME from a claim to an emptyDir", persistence: map[string]any{"enabled": false},
			live: map[string]string{upgradeHookSelectorNameQuery: "other",
				upgradeHookPodHomeQuery: "cetirun-example-0/home-example"},
			output: "HOME of the running Pod is in the claim home-example.\n" +
				"HOME of a new Pod would be in an emptyDir, without the files of the running one.",
			absent: "--cascade=orphan"},
		{name: "changed selector, HOME from an emptyDir to the claim of the chart",
			persistence: map[string]any{"useStatefulSetVolumeClaim": false},
			live:        map[string]string{upgradeHookSelectorNameQuery: "other", upgradeHookPodHomeQuery: "cetirun-example-0/"},
			output: "HOME of the running Pod is in an emptyDir.\n" +
				"HOME of a new Pod would be in the claim cetirun-example-data, without the files of the running one.",
			absent: "--cascade=orphan"},
		// Helm deletes a claim that the chart created and the new values no longer create, also when they name it as
		// an existing claim. Reverting only the selector would not keep it: the values that create it go back too, or
		// the claim is kept out of the deletions of Helm first.
		{name: "changed selector, the claim of the chart named as an existing claim",
			persistence: map[string]any{"useStatefulSetVolumeClaim": false, "existingClaim": "cetirun-example-data"},
			live:        map[string]string{upgradeHookSelectorNameQuery: "other"},
			output: "HOME of the running Pod is in the claim cetirun-example-data, which the chart creates. " +
				"The new values do not\ncreate it, so an upgrade with them deletes it, with its files, if this release " +
				"created it. Before the\nupgrade runs again, set back the persistence values that create it too, or first " +
				"keep the claim out of\nthe deletions of Helm:\n" +
				"kubectl annotate pvc cetirun-example-data -n cetirun-example helm.sh/resource-policy=keep\n",
			absent: "--cascade=orphan"},
		{name: "changed selector, HOME from the claim of the chart to the claim template",
			live: map[string]string{upgradeHookSelectorNameQuery: "other", upgradeHookClaimNamesQuery: "",
				upgradeHookPodHomeQuery: "cetirun-example-0/cetirun-example-data"},
			output: "HOME of the running Pod is in the claim cetirun-example-data, which the chart creates.",
			absent: "--cascade=orphan"},
		{name: "changed selector and grown volume",
			live:   map[string]string{upgradeHookInstanceQuery: "other", upgradeHookSizeQuery: "100Gi"},
			output: "selector app.kubernetes.io/instance: 'other' -> 'cetirun-example'; volumeClaimTemplate size"},
		{name: "failed read", live: map[string]string{upgradeHookSizeQuery: "100Gi"}, failQuery: upgradeHookServiceNameQuery,
			output: "unable to return a response"},
		{name: "failed read of the HOME of the Pod", persistence: map[string]any{"existingClaim": "home-example"},
			live: map[string]string{upgradeHookSelectorNameQuery: "other"}, failQuery: upgradeHookPodHomeQuery,
			output: "unable to return a response", absent: "--cascade=orphan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := upgradeHookInput("hermes")
			persistence := object(input["persistence"])
			for key, value := range tc.persistence {
				persistence[key] = value
			}
			script := upgradeHookScript(t, upgradeHookJob(t, input))
			fields := liveStatefulSetFields(t, upgradeStatefulSet(t, input))
			for query, value := range tc.live {
				fields[query] = value
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
			if tc.absent != "" && strings.Contains(string(out), tc.absent) {
				t.Fatalf("output has %q:\n%s", tc.absent, out)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			name := regexp.MustCompile(`STS_NAME="([^"]+)"`).FindStringSubmatch(script)
			if name == nil {
				t.Fatal("the hook script names no StatefulSet")
			}
			deleted := strings.Contains(string(calls), "delete statefulset "+name[1]+" -n cetirun-example --cascade=orphan")
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
