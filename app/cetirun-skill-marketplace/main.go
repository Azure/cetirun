// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Command cetirun-skill-marketplace synchronizes one Git repository of skills,
// publishes each policy-bound Team's selection as an immutable release on
// shared Blob NFS storage, gives each Agent namespace a read-only claim of that
// storage, and writes the Skill Marketplace catalog.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/gitsync"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/publish"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Version and Commit are set at build time.
var (
	Version = "dev"
	Commit  = "unknown"
)

type options struct {
	source            gitsync.Source
	path              string
	syncInterval      time.Duration
	storageRoot       string
	storage           controller.StorageSource
	claimName         string
	claimSize         string
	catalogNamespace  string
	reserved          string
	teamGCGrace       time.Duration
	maxRepositorySize string
	maxRepository     int64
	maxObjects        int64
	maxSkillSize      string
	maxSkillMDSize    string
	limits            skills.Limits
	healthAddress     string
	metricsAddress    string
	authMode          string
	credentialsDir    string
	allowInsecureHTTP bool
}

// size is the default flag text of a size limit, such as 5Mi.
func size(bytes int64) string {
	return resource.NewQuantity(bytes, resource.BinarySI).String()
}

func parse(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("cetirun-skill-marketplace", flag.ContinueOnError)
	fs.StringVar(&o.source.URL, "repository-url", "", "Git repository URL (https://, or ssh with --auth=ssh)")
	fs.StringVar(&o.source.Ref, "repository-ref", "", "branch, tag or full ref; empty uses the remote HEAD")
	fs.StringVar(&o.path, "repository-path", "", "directory of the repository that holds the skills")
	fs.StringVar(&o.authMode, "auth", "none", "none, token or ssh")
	fs.StringVar(&o.source.TokenUsername, "token-username", "x-access-token", "user name sent with the token")
	fs.StringVar(&o.credentialsDir, "credentials-dir", "/var/run/cetirun/git", "directory of the mounted credentials Secret")
	fs.BoolVar(&o.allowInsecureHTTP, "allow-insecure-http", false, "allow an http:// repository URL (tests only)")
	fs.DurationVar(&o.syncInterval, "sync-interval", 5*time.Minute, "how often the repository is checked")
	fs.StringVar(&o.storageRoot, "storage-root", "/var/lib/cetirun-skills", "read-write mount of the marketplace container")
	fs.StringVar(&o.storage.ResourceGroup, "storage-resource-group", "", "resource group of the storage account")
	fs.StringVar(&o.storage.StorageAccount, "storage-account", "", "storage account of the marketplace container")
	fs.StringVar(&o.storage.ContainerName, "storage-container", "", "Blob NFS container of the marketplace")
	fs.StringVar(&o.storage.StorageClassName, "storage-class", "azureblob-nfs-premium", "StorageClass name of the Agent volumes")
	fs.StringVar(&o.claimName, "claim-name", "skills", "name of the skills claim in each Agent namespace")
	fs.StringVar(&o.claimSize, "claim-size", "1Gi", "requested size of each Agent's skills claim")
	fs.StringVar(&o.catalogNamespace, "catalog-namespace", "", "namespace of the catalog ConfigMap (the operator namespace)")
	fs.StringVar(&o.reserved, "reserved-names", strings.Join(skills.DefaultReserved, ","), "comma-separated reserved skill names")
	fs.DurationVar(&o.teamGCGrace, "team-gc-grace", time.Hour, "how long a deleted Team's releases stay after its last publication")
	defaults := skills.DefaultLimits
	fs.StringVar(&o.maxRepositorySize, "max-repository-size", "256Mi", "largest repository commit to load: the pack that the Git server sends, and its objects together")
	fs.Int64Var(&o.maxObjects, "max-repository-objects", 100000, "most objects (files, directories and the commit) in the repository commit")
	fs.IntVar(&o.limits.MaxFiles, "max-skill-files", defaults.MaxFiles, "most files in one skill")
	fs.StringVar(&o.maxSkillSize, "max-skill-size", size(defaults.MaxBytes), "largest total size of the files of one skill")
	fs.StringVar(&o.maxSkillMDSize, "max-skill-md-size", size(defaults.MaxSkillMD), "largest SKILL.md")
	fs.IntVar(&o.limits.MaxDepth, "max-skill-depth", defaults.MaxDepth, "deepest directory level of a file in a skill")
	fs.IntVar(&o.limits.MaxDescription, "max-description-length", defaults.MaxDescription, "longest SKILL.md description, in characters")
	fs.StringVar(&o.healthAddress, "health-address", ":8081", "health probe address")
	fs.StringVar(&o.metricsAddress, "metrics-address", ":8080", "metrics address; 0 disables")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.source.Auth = gitsync.Auth(o.authMode)
	o.source.CredentialsDir = o.credentialsDir
	o.source.AllowInsecureHTTP = o.allowInsecureHTTP
	if err := o.source.Validate(); err != nil {
		return o, err
	}
	if o.path != "" && (strings.HasPrefix(o.path, "/") || strings.Contains(o.path, "..")) {
		return o, fmt.Errorf("--repository-path must be relative to the repository root")
	}
	if o.syncInterval < 30*time.Second {
		return o, fmt.Errorf("--sync-interval must be at least 30s")
	}
	for name, value := range map[string]string{
		"--storage-resource-group": o.storage.ResourceGroup, "--storage-account": o.storage.StorageAccount,
		"--storage-container": o.storage.ContainerName, "--catalog-namespace": o.catalogNamespace,
	} {
		if value == "" {
			return o, fmt.Errorf("%s is required", name)
		}
	}
	if len(validation.IsDNS1123Label(o.claimName)) != 0 || len(validation.IsDNS1123Label(o.catalogNamespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(o.storage.StorageClassName)) != 0 {
		return o, fmt.Errorf("invalid claim name, catalog namespace or StorageClass name")
	}
	if _, err := resource.ParseQuantity(o.claimSize); err != nil {
		return o, fmt.Errorf("invalid --claim-size: %w", err)
	}
	for _, limit := range []struct {
		flag, value string
		bytes       *int64
	}{
		{"--max-repository-size", o.maxRepositorySize, &o.maxRepository},
		{"--max-skill-size", o.maxSkillSize, &o.limits.MaxBytes},
		{"--max-skill-md-size", o.maxSkillMDSize, &o.limits.MaxSkillMD},
	} {
		quantity, err := resource.ParseQuantity(limit.value)
		if err != nil || quantity.Sign() <= 0 {
			return o, fmt.Errorf("%s must be a positive size, such as 5Mi", limit.flag)
		}
		*limit.bytes = quantity.Value()
	}
	for _, limit := range []struct {
		flag  string
		value int
	}{
		{"--max-skill-files", o.limits.MaxFiles}, {"--max-skill-depth", o.limits.MaxDepth},
		{"--max-description-length", o.limits.MaxDescription},
	} {
		if limit.value < 1 {
			return o, fmt.Errorf("%s must be at least 1", limit.flag)
		}
	}
	if o.maxObjects < 1 {
		return o, fmt.Errorf("--max-repository-objects must be at least 1")
	}
	if o.limits.MaxSkillMD > o.limits.MaxBytes {
		return o, fmt.Errorf("--max-skill-md-size cannot be larger than --max-skill-size")
	}
	return o, nil
}

func main() {
	ctrl.SetLogger(zap.New())
	logger := ctrl.Log.WithName("skill-marketplace")
	o, err := parse(os.Args[1:])
	if err != nil {
		logger.Error(err, "invalid configuration")
		os.Exit(2)
	}
	if limit := setMemoryLimit("/sys/fs/cgroup/memory.max"); limit > 0 {
		logger.Info("set the Go memory limit from the container's memory limit", "bytes", limit)
	}
	if err := run(ctrl.SetupSignalHandler(), o); err != nil {
		logger.Error(err, "the Skill Marketplace stopped")
		os.Exit(1)
	}
}

// setMemoryLimit gives the Go runtime a soft memory limit of 90% of the
// container's memory limit, unless GOMEMLIMIT sets one: the garbage collector
// then runs before a fetch near the repository limit reaches the container
// limit. It returns the limit that it set, or 0.
func setMemoryLimit(cgroupFile string) int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0
	}
	raw, err := os.ReadFile(cgroupFile)
	if err != nil {
		return 0
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || limit <= 0 {
		return 0
	}
	limit = limit / 10 * 9
	debug.SetMemoryLimit(limit)
	return limit
}

func run(ctx context.Context, o options) error {
	logger := ctrl.Log.WithName("skill-marketplace")
	logger.Info("starting", "version", Version, "commit", Commit, "repository", gitsync.Redacted(o.source.URL),
		"ref", o.source.Ref, "path", o.path, "maxRepositoryBytes", o.maxRepository, "maxRepositoryObjects", o.maxObjects,
		"maxSkillFiles", o.limits.MaxFiles, "maxSkillBytes", o.limits.MaxBytes, "maxSkillMDBytes", o.limits.MaxSkillMD,
		"maxSkillDepth", o.limits.MaxDepth, "maxDescriptionLength", o.limits.MaxDescription)
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	publisher := &publish.Publisher{Root: o.storageRoot}
	if err := publisher.Init(); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: o.healthAddress,
		Metrics:                metricsserver.Options{BindAddress: o.metricsAddress},
	})
	if err != nil {
		return err
	}
	reader := mgr.GetAPIReader()
	catalog := controller.NewCatalog(mgr.GetClient(), reader, o.catalogNamespace, api.SkillMarketplaceRepository{
		URL: gitsync.Redacted(o.source.URL), Ref: o.source.Ref, Path: o.path,
	})
	if err := catalog.Seed(ctx); err != nil {
		logger.Error(err, "read the previous catalog")
	}
	var reserved []string
	for _, name := range strings.Split(o.reserved, ",") {
		if name = strings.TrimSpace(name); name != "" {
			reserved = append(reserved, name)
		}
	}
	store := &controller.Store{}
	changed := make(chan event.GenericEvent, 1)
	syncer := newSyncer(o, reserved, store, catalog, changed)
	teams := &controller.TeamReconciler{Client: mgr.GetClient(), Publisher: publisher, Store: store, Catalog: catalog, GCGrace: o.teamGCGrace}
	claims := &controller.ClaimReconciler{
		Client: mgr.GetClient(), Reader: reader, Storage: o.storage, ClaimName: o.claimName,
		ClaimSize: resource.MustParse(o.claimSize), Publisher: publisher,
	}
	collector := &controller.VolumeCollector{Client: mgr.GetClient(), Reader: reader, Interval: 5 * time.Minute}
	for _, runnable := range []interface {
		Start(context.Context) error
	}{syncer, catalog, collector} {
		if err := mgr.Add(runnableFunc(runnable.Start)); err != nil {
			return err
		}
	}
	if err := teams.SetupWithManager(mgr, changed); err != nil {
		return err
	}
	if err := claims.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

type runnableFunc func(context.Context) error

func (f runnableFunc) Start(ctx context.Context) error { return f(ctx) }

// newSyncer returns the repository synchronizer with the configured limits.
func newSyncer(o options, reserved []string, store *controller.Store, catalog *controller.Catalog,
	changed chan event.GenericEvent) *controller.Syncer {
	return &controller.Syncer{
		Source: o.source, Path: o.path, Interval: o.syncInterval, Limits: o.limits, Reserved: reserved,
		Fetch: gitsync.Limits{MaxBytes: o.maxRepository, MaxObjects: o.maxObjects},
		Store: store, Catalog: catalog, Changed: changed,
	}
}
