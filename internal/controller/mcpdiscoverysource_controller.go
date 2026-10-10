package controller

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	mcpv1alpha2 "github.com/mcp-hangar/operator/api/v1alpha2"
	"github.com/mcp-hangar/operator/pkg/metrics"
)

const (
	// LabelDiscoveryManagedBy identifies MCPServers created by a discovery source
	LabelDiscoveryManagedBy = "mcp-hangar.io/managed-by"

	// Condition types for discovery sources
	ConditionSynced = "Synced"
	ConditionPaused = "Paused"

	// Default refresh interval for rescanning
	defaultRefreshInterval = 1 * time.Minute

	// Paused requeue interval
	pausedRequeueAfter = 5 * time.Minute

	// Default excluded namespaces for namespace discovery
	defaultExcludeKubeSystem    = "kube-system"
	defaultExcludeKubePublic    = "kube-public"
	defaultExcludeKubeNodeLease = "kube-node-lease"

	// Default annotation prefix and required annotation
	defaultAnnotationPrefix   = "mcp-hangar.io"
	defaultRequiredAnnotation = "mcp-hangar.io/provider"
	annotationEndpointKey     = "mcp-hangar.io/endpoint"

	// Default service discovery settings
	defaultPortName = "mcp"
	defaultProtocol = "http"

	// Default ConfigMap key
	defaultConfigMapKey = "providers.yaml"

	// Event reasons for discovery
	ReasonSyncStarted   = "SyncStarted"
	ReasonSyncCompleted = "SyncCompleted"
	ReasonSyncFailed    = "SyncFailed"
	ReasonProviderFound = "ProviderFound"
	ReasonProviderGone  = "ProviderRemoved"

	// ReasonCrossNamespaceRefused is the Synced/Ready reason and Warning event
	// reason for a ConfigMap source whose configMapRef names another namespace.
	ReasonCrossNamespaceRefused = "CrossNamespaceRefused"
)

// DiscoveredMCPServerInfo holds information about a discovered provider
type DiscoveredMCPServerInfo struct {
	Name     string
	Source   string
	Endpoint string
	Mode     mcpv1alpha2.MCPServerMode
	// Image, Command and Args are set by ConfigMap entries only; the other
	// discovery types find remote endpoints and leave them empty (#206).
	Image   string
	Command []string
	Args    []string
	Labels  map[string]string
}

// ConfigMapMCPServerEntry defines a provider entry in a ConfigMap
type ConfigMapMCPServerEntry struct {
	Mode     string   `yaml:"mode"`
	Image    string   `yaml:"image,omitempty"`
	Endpoint string   `yaml:"endpoint,omitempty"`
	Command  []string `yaml:"command,omitempty"`
	Args     []string `yaml:"args,omitempty"`
}

// MCPDiscoverySourceReconciler reconciles a MCPDiscoverySource object
type MCPDiscoverySourceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// APIReader reads ConfigMaps and Services straight from the API server.
	// Read through the manager's client, each kind started a cluster-wide
	// informer holding every ConfigMap or Service in the cluster -- other
	// people's included -- to answer a question asked once per sync (#195).
	// Nil falls back to the client (tests).
	APIReader client.Reader
}

// uncached returns the reader for kinds this controller only lists once per
// sync and never watches.
func (r *MCPDiscoverySourceReconciler) uncached() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpdiscoverysources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpdiscoverysources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpdiscoverysources/finalizers,verbs=update
// +kubebuilder:rbac:groups=mcp-hangar.io,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile performs the reconciliation loop for MCPDiscoverySource
func (r *MCPDiscoverySourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Reconciling MCPDiscoverySource", "namespacedName", req.NamespacedName)

	// Fetch the MCPDiscoverySource instance
	source := &mcpv1alpha2.MCPDiscoverySource{}
	if err := r.Get(ctx, req.NamespacedName, source); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("MCPDiscoverySource resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPDiscoverySource")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !source.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, source)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(source, finalizerName) {
		controllerutil.AddFinalizer(source, finalizerName)
		if err := r.Update(ctx, source); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Main reconciliation logic
	return r.reconcileNormal(ctx, source)
}

// reconcileNormal handles normal (non-deletion) reconciliation
func (r *MCPDiscoverySourceReconciler) reconcileNormal(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Paused check FIRST -- if paused, freeze all operations
	if source.IsPaused() {
		logger.Info("MCPDiscoverySource is paused, skipping sync")
		setCondition(source, ConditionPaused, metav1.ConditionTrue, "Paused", "Discovery is paused")
		setCondition(source, ConditionSynced, metav1.ConditionUnknown, "Paused", "Sync suspended while paused")
		if err := r.Status().Update(ctx, source); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: pausedRequeueAfter}, nil
	}

	// Clear paused condition
	setCondition(source, ConditionPaused, metav1.ConditionFalse, "Active", "Discovery is active")

	// Update ObservedGeneration
	source.Status.ObservedGeneration = source.Generation

	// A configMapRef into another namespace is refused before anything is
	// read (#234): its entries would become MCPServers -- running containers
	// since #233 -- in this namespace, with the operator's ServiceAccount as
	// the confused deputy. This holds with the admission webhook off.
	if cmNamespace, refused := crossNamespaceConfigMap(source); refused {
		return r.refuseCrossNamespace(ctx, source, cmNamespace)
	}

	// v1alpha2 carries the interval pre-parsed; non-positive falls back.
	refreshInterval := defaultRefreshInterval
	if source.Spec.RefreshInterval != nil && source.Spec.RefreshInterval.Duration > 0 {
		refreshInterval = source.Spec.RefreshInterval.Duration
	}

	// Start sync timer
	syncStart := time.Now()
	r.Recorder.Eventf(source, nil, corev1.EventTypeNormal, ReasonSyncStarted, ActionReconcile,
		"Starting discovery sync")

	// Discover providers
	discovered, scanErrors, err := r.discoverProviders(ctx, source)
	if err != nil {
		logger.Error(err, "Discovery failed completely")
		source.Status.LastSyncError = err.Error()
		setCondition(source, ConditionSynced, metav1.ConditionFalse, "DiscoveryFailed", err.Error())
		setCondition(source, ConditionReady, metav1.ConditionFalse, "DiscoveryFailed", err.Error())
		if statusErr := r.Status().Update(ctx, source); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		r.Recorder.Eventf(source, nil, corev1.EventTypeWarning, ReasonSyncFailed, ActionReconcile,
			"Discovery failed: %v", err)
		return ctrl.Result{RequeueAfter: errorRequeueAfter}, nil
	}

	// Apply filters
	discovered = r.applyFilters(source, discovered)

	// Create or update providers
	var createErrors []string
	managedCount := int32(0)
	discoveredProviderStatuses := make([]mcpv1alpha2.DiscoveredMCPServer, 0, len(discovered))

	for name, info := range discovered {
		dp := mcpv1alpha2.DiscoveredMCPServer{
			Name:         name,
			Source:       info.Source,
			DiscoveredAt: metav1.Now(),
			Managed:      true,
		}

		// The name comes from an annotation value or a ConfigMap key. Refuse
		// one Kubernetes would refuse anyway, here and with a reason, instead
		// of a Create that fails per entry (#213).
		if err := validateDiscoveredName(name); err != nil {
			createErrors = append(createErrors, fmt.Sprintf("%s: %v", name, err))
			dp.Managed = false
			dp.Error = err.Error()
			discoveredProviderStatuses = append(discoveredProviderStatuses, dp)
			continue
		}

		if err := r.createOrUpdateMCPServer(ctx, source, info); err != nil {
			logger.Error(err, "Failed to create/update provider", "provider", name)
			createErrors = append(createErrors, fmt.Sprintf("%s: %v", name, err))
			dp.Managed = false
			dp.Error = err.Error()
		} else {
			managedCount++
		}

		discoveredProviderStatuses = append(discoveredProviderStatuses, dp)
	}

	// Authoritative sync: delete providers no longer discovered
	if source.IsAuthoritative() {
		if deleteErrs := r.authoritativeSync(ctx, source, discovered); len(deleteErrs) > 0 {
			createErrors = append(createErrors, deleteErrs...)
		}
	}

	// Update status
	syncDuration := time.Since(syncStart)
	now := metav1.Now()
	nextSync := metav1.NewTime(now.Add(refreshInterval))

	source.Status.DiscoveredCount = int32(len(discovered))
	source.Status.ManagedCount = managedCount
	source.Status.LastSyncTime = &now
	source.Status.LastSyncDuration = &metav1.Duration{Duration: syncDuration}
	source.Status.NextSyncTime = &nextSync
	source.Status.DiscoveredMCPServers = discoveredProviderStatuses

	// Collect all errors
	allErrors := append(scanErrors, createErrors...)
	if len(allErrors) > 0 {
		source.Status.LastSyncError = strings.Join(allErrors, "; ")
		setCondition(source, ConditionSynced, metav1.ConditionFalse, "PartialFailure", source.Status.LastSyncError)
	} else {
		source.Status.LastSyncError = ""
		setCondition(source, ConditionSynced, metav1.ConditionTrue, "SyncCompleted", fmt.Sprintf("Discovered %d providers", len(discovered)))
	}

	// Ready condition: True if sync succeeded even partially (providers were discovered)
	if len(discovered) > 0 || len(allErrors) == 0 {
		setCondition(source, ConditionReady, metav1.ConditionTrue, "Ready", fmt.Sprintf("Managing %d providers", managedCount))
	} else {
		setCondition(source, ConditionReady, metav1.ConditionFalse, "NoProviders", "No providers discovered")
	}

	// Update metrics
	metrics.DiscoverySourceCount.WithLabelValues(source.Namespace, source.Name).Set(float64(len(discovered)))
	metrics.DiscoverySyncDuration.WithLabelValues(source.Namespace, source.Name).Observe(syncDuration.Seconds())

	if err := r.Status().Update(ctx, source); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(source, nil, corev1.EventTypeNormal, ReasonSyncCompleted, ActionReconcile,
		"Sync completed: discovered=%d, managed=%d, errors=%d", len(discovered), managedCount, len(allErrors))

	return ctrl.Result{RequeueAfter: refreshInterval}, nil
}

// discoverProviders routes to the appropriate discovery method based on type
func (r *MCPDiscoverySourceReconciler) discoverProviders(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (map[string]DiscoveredMCPServerInfo, []string, error) {
	switch source.Spec.Type {
	case mcpv1alpha2.DiscoveryTypeNamespace:
		return r.discoverNamespace(ctx, source)
	case mcpv1alpha2.DiscoveryTypeConfigMap:
		return r.discoverConfigMap(ctx, source)
	case mcpv1alpha2.DiscoveryTypeAnnotations:
		return r.discoverAnnotations(ctx, source)
	case mcpv1alpha2.DiscoveryTypeServiceDiscovery:
		return r.discoverServices(ctx, source)
	default:
		// defense-in-depth: unreachable while the CRD schema enforces spec.type
		// via +kubebuilder:validation:Enum=Namespace;ConfigMap;Annotations;ServiceDiscovery,
		// so a persisted source can never carry an unknown type. Kept as a guard
		// against future enum additions or direct-cache manipulation.
		return nil, nil, fmt.Errorf("unknown discovery type: %s", source.Spec.Type)
	}
}

// discoverNamespace discovers providers by scanning namespaces matching labels
func (r *MCPDiscoverySourceReconciler) discoverNamespace(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (map[string]DiscoveredMCPServerInfo, []string, error) {
	logger := log.FromContext(ctx)
	discovered := make(map[string]DiscoveredMCPServerInfo)
	var scanErrors []string

	if source.Spec.NamespaceSelector == nil {
		return discovered, nil, nil
	}

	// Build list options from MatchLabels
	listOpts := []client.ListOption{}
	if len(source.Spec.NamespaceSelector.MatchLabels) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(source.Spec.NamespaceSelector.MatchLabels))
	}

	// List namespaces
	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, listOpts...); err != nil {
		return nil, nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	// Build exclude set
	excludeSet := map[string]bool{
		defaultExcludeKubeSystem:    true,
		defaultExcludeKubePublic:    true,
		defaultExcludeKubeNodeLease: true,
	}
	if source.Spec.NamespaceSelector.ExcludeNamespaces != nil {
		excludeSet = make(map[string]bool)
		for _, ns := range source.Spec.NamespaceSelector.ExcludeNamespaces {
			excludeSet[ns] = true
		}
	}

	// Build expression selector once (if any)
	var exprSelector labels.Selector
	if len(source.Spec.NamespaceSelector.MatchExpressions) > 0 {
		ls := &metav1.LabelSelector{
			MatchExpressions: source.Spec.NamespaceSelector.MatchExpressions,
		}
		var selectorErr error
		exprSelector, selectorErr = metav1.LabelSelectorAsSelector(ls)
		if selectorErr != nil {
			return nil, []string{fmt.Sprintf("invalid match expressions: %v", selectorErr)}, nil
		}
	}

	// Check MatchExpressions if set
	for _, ns := range nsList.Items {
		if excludeSet[ns.Name] {
			continue
		}

		// Check MatchExpressions via standard labels.Selector
		if exprSelector != nil && !exprSelector.Matches(labels.Set(ns.Labels)) {
			continue
		}

		providerName := fmt.Sprintf("%s-%s", source.Name, ns.Name)
		discovered[providerName] = DiscoveredMCPServerInfo{
			Name:   providerName,
			Source: fmt.Sprintf("namespace/%s", ns.Name),
			Mode:   mcpv1alpha2.MCPServerModeRemote,
			Labels: map[string]string{
				"discovery-namespace": ns.Name,
			},
		}
		logger.Info("Discovered provider from namespace", "namespace", ns.Name, "provider", providerName)
	}

	return discovered, scanErrors, nil
}

// discoverConfigMap discovers providers from a ConfigMap containing YAML definitions
func (r *MCPDiscoverySourceReconciler) discoverConfigMap(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (map[string]DiscoveredMCPServerInfo, []string, error) {
	logger := log.FromContext(ctx)
	discovered := make(map[string]DiscoveredMCPServerInfo)

	if source.Spec.ConfigMapRef == nil {
		return discovered, nil, nil
	}

	// The ConfigMap is always read from the source's own namespace.
	// reconcileNormal refuses a cross-namespace reference first; this guard
	// keeps any other caller from reading one (#234).
	if cmNamespace, refused := crossNamespaceConfigMap(source); refused {
		return nil, nil, fmt.Errorf("configMapRef namespace %q differs from the source namespace %q", cmNamespace, source.Namespace)
	}
	cmNamespace := source.Namespace

	// Determine ConfigMap key
	cmKey := source.Spec.ConfigMapRef.Key
	if cmKey == "" {
		cmKey = defaultConfigMapKey
	}

	// Fetch ConfigMap
	cm := &corev1.ConfigMap{}
	cmObjKey := client.ObjectKey{Name: source.Spec.ConfigMapRef.Name, Namespace: cmNamespace}
	if err := r.uncached().Get(ctx, cmObjKey, cm); err != nil {
		return nil, nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", cmNamespace, source.Spec.ConfigMapRef.Name, err)
	}

	// Read provider definitions from the key
	data, ok := cm.Data[cmKey]
	if !ok {
		return nil, []string{fmt.Sprintf("key %q not found in ConfigMap %s/%s", cmKey, cmNamespace, source.Spec.ConfigMapRef.Name)}, nil
	}

	// Parse YAML
	var entries map[string]ConfigMapMCPServerEntry
	if err := yaml.Unmarshal([]byte(data), &entries); err != nil {
		return nil, nil, fmt.Errorf("failed to parse providers YAML from ConfigMap: %w", err)
	}

	for name, entry := range entries {
		providerName := fmt.Sprintf("%s-%s", source.Name, name)
		mode := mcpv1alpha2.MCPServerModeRemote
		if entry.Mode == "container" {
			mode = mcpv1alpha2.MCPServerModeContainer
		}

		discovered[providerName] = DiscoveredMCPServerInfo{
			Name:     providerName,
			Source:   fmt.Sprintf("configmap/%s", source.Spec.ConfigMapRef.Name),
			Endpoint: entry.Endpoint,
			Mode:     mode,
			Image:    entry.Image,
			Command:  entry.Command,
			Args:     entry.Args,
			Labels: map[string]string{
				"discovery-configmap": source.Spec.ConfigMapRef.Name,
				"discovery-entry":     name,
			},
		}
		logger.Info("Discovered provider from ConfigMap", "configmap", source.Spec.ConfigMapRef.Name, "entry", name, "provider", providerName)
	}

	return discovered, nil, nil
}

// crossNamespaceConfigMap reports whether a ConfigMap source references a
// ConfigMap outside its own namespace, and returns that namespace.
func crossNamespaceConfigMap(source *mcpv1alpha2.MCPDiscoverySource) (string, bool) {
	if source.Spec.Type != mcpv1alpha2.DiscoveryTypeConfigMap || source.Spec.ConfigMapRef == nil {
		return "", false
	}
	ns := source.Spec.ConfigMapRef.Namespace
	return ns, ns != "" && ns != source.Namespace
}

// refuseCrossNamespace reports a refused cross-namespace configMapRef. It reads
// nothing, creates nothing and deletes nothing: servers the source created
// before the refusal are left as they are, so the message says so. The
// Warning event is emitted once, on the transition into the refusal.
func (r *MCPDiscoverySourceReconciler) refuseCrossNamespace(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource, cmNamespace string) (ctrl.Result, error) {
	msg := fmt.Sprintf("configMapRef names ConfigMap %s/%s but this source is in namespace %q; "+
		"a ConfigMap source may only read its own namespace. Nothing was read or created; "+
		"MCPServers this source created earlier are left as they are",
		cmNamespace, source.Spec.ConfigMapRef.Name, source.Namespace)

	prev := apimeta.FindStatusCondition(source.Status.Conditions, ConditionSynced)
	transition := prev == nil || prev.Reason != ReasonCrossNamespaceRefused

	source.Status.LastSyncError = msg
	setCondition(source, ConditionSynced, metav1.ConditionFalse, ReasonCrossNamespaceRefused, msg)
	setCondition(source, ConditionReady, metav1.ConditionFalse, ReasonCrossNamespaceRefused, msg)
	if err := r.Status().Update(ctx, source); err != nil {
		return ctrl.Result{}, err
	}
	if transition {
		log.FromContext(ctx).Info("Refusing cross-namespace configMapRef",
			"configMapNamespace", cmNamespace, "sourceNamespace", source.Namespace)
		r.Recorder.Eventf(source, nil, corev1.EventTypeWarning, ReasonCrossNamespaceRefused, ActionReconcile,
			"%s", msg)
	}
	// No requeue: fixing the reference is a spec change, which reconciles.
	return ctrl.Result{}, nil
}

// discoverAnnotations discovers providers from annotated Pods and Services
func (r *MCPDiscoverySourceReconciler) discoverAnnotations(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (map[string]DiscoveredMCPServerInfo, []string, error) {
	logger := log.FromContext(ctx)
	discovered := make(map[string]DiscoveredMCPServerInfo)
	var scanErrors []string

	if source.Spec.Annotations == nil {
		return discovered, nil, nil
	}

	annotationPrefix := source.Spec.Annotations.AnnotationPrefix
	if annotationPrefix == "" {
		annotationPrefix = defaultAnnotationPrefix
	}

	requiredAnnotations := source.Spec.Annotations.RequiredAnnotations
	if len(requiredAnnotations) == 0 {
		requiredAnnotations = []string{defaultRequiredAnnotation}
	}

	providerAnnotation := requiredAnnotations[0]

	// Discover from Pods
	if len(source.Spec.Annotations.PodSelector) > 0 {
		podList := &corev1.PodList{}
		if err := r.List(ctx, podList,
			client.InNamespace(source.Namespace),
			client.MatchingLabels(source.Spec.Annotations.PodSelector),
		); err != nil {
			scanErrors = append(scanErrors, fmt.Sprintf("failed to list pods: %v", err))
		} else {
			for _, pod := range podList.Items {
				if !hasRequiredAnnotations(pod.Annotations, requiredAnnotations) {
					continue
				}

				providerName := pod.Annotations[providerAnnotation]
				if providerName == "" {
					providerName = pod.Name
				}

				endpoint := pod.Annotations[annotationEndpointKey]
				if endpoint == "" {
					// Construct from pod IP
					if pod.Status.PodIP != "" {
						endpoint = fmt.Sprintf("http://%s:8080", pod.Status.PodIP)
					}
				}

				fullName := fmt.Sprintf("%s-%s", source.Name, providerName)
				discovered[fullName] = DiscoveredMCPServerInfo{
					Name:     fullName,
					Source:   fmt.Sprintf("annotation/Pod/%s", pod.Name),
					Endpoint: endpoint,
					Mode:     mcpv1alpha2.MCPServerModeRemote,
					Labels: map[string]string{
						"discovery-kind":     "Pod",
						"discovery-resource": pod.Name,
					},
				}
				logger.Info("Discovered provider from Pod annotation", "pod", pod.Name, "provider", fullName)
			}
		}
	}

	// Discover from Services
	if len(source.Spec.Annotations.ServiceSelector) > 0 {
		svcList := &corev1.ServiceList{}
		if err := r.uncached().List(ctx, svcList,
			client.InNamespace(source.Namespace),
			client.MatchingLabels(source.Spec.Annotations.ServiceSelector),
		); err != nil {
			scanErrors = append(scanErrors, fmt.Sprintf("failed to list services: %v", err))
		} else {
			for _, svc := range svcList.Items {
				if !hasRequiredAnnotations(svc.Annotations, requiredAnnotations) {
					continue
				}

				providerName := svc.Annotations[providerAnnotation]
				if providerName == "" {
					providerName = svc.Name
				}

				endpoint := svc.Annotations[annotationEndpointKey]
				if endpoint == "" {
					// Construct from service
					port := int32(8080)
					for _, p := range svc.Spec.Ports {
						if p.Name == defaultPortName || p.Name == "http" {
							port = p.Port
							break
						}
					}
					endpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace, port)
				}

				fullName := fmt.Sprintf("%s-%s", source.Name, providerName)
				discovered[fullName] = DiscoveredMCPServerInfo{
					Name:     fullName,
					Source:   fmt.Sprintf("annotation/Service/%s", svc.Name),
					Endpoint: endpoint,
					Mode:     mcpv1alpha2.MCPServerModeRemote,
					Labels: map[string]string{
						"discovery-kind":     "Service",
						"discovery-resource": svc.Name,
					},
				}
				logger.Info("Discovered provider from Service annotation", "service", svc.Name, "provider", fullName)
			}
		}
	}

	return discovered, scanErrors, nil
}

// discoverServices discovers providers from Kubernetes Services matching a label selector
func (r *MCPDiscoverySourceReconciler) discoverServices(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (map[string]DiscoveredMCPServerInfo, []string, error) {
	logger := log.FromContext(ctx)
	discovered := make(map[string]DiscoveredMCPServerInfo)

	if source.Spec.ServiceDiscovery == nil {
		return discovered, nil, nil
	}

	portName := source.Spec.ServiceDiscovery.PortName
	if portName == "" {
		portName = defaultPortName
	}

	protocol := source.Spec.ServiceDiscovery.Protocol
	if protocol == "" {
		protocol = defaultProtocol
	}

	// List services matching selector
	svcList := &corev1.ServiceList{}
	listOpts := []client.ListOption{
		client.InNamespace(source.Namespace),
	}
	if len(source.Spec.ServiceDiscovery.Selector) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(source.Spec.ServiceDiscovery.Selector))
	}

	if err := r.uncached().List(ctx, svcList, listOpts...); err != nil {
		return nil, nil, fmt.Errorf("failed to list services: %w", err)
	}

	for _, svc := range svcList.Items {
		// Find the named port
		var svcPort int32
		found := false
		for _, p := range svc.Spec.Ports {
			if p.Name == portName {
				svcPort = p.Port
				found = true
				break
			}
		}

		if !found {
			continue
		}

		endpoint := fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d", protocol, svc.Name, svc.Namespace, svcPort)
		providerName := fmt.Sprintf("%s-%s", source.Name, svc.Name)

		discovered[providerName] = DiscoveredMCPServerInfo{
			Name:     providerName,
			Source:   fmt.Sprintf("service/%s", svc.Name),
			Endpoint: endpoint,
			Mode:     mcpv1alpha2.MCPServerModeRemote,
			Labels: map[string]string{
				"discovery-service": svc.Name,
			},
		}
		logger.Info("Discovered provider from Service", "service", svc.Name, "endpoint", endpoint, "provider", providerName)
	}

	return discovered, nil, nil
}

// createOrUpdateMCPServer creates or updates an MCPServer CR for a discovered provider
func (r *MCPDiscoverySourceReconciler) createOrUpdateMCPServer(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource, info DiscoveredMCPServerInfo) error {
	// A container entry with no image from either the entry or the template
	// would only become an MCPServer the server controller marks Dead
	// (InvalidSpec). Refuse it here so the source status names the entry
	// instead of a Dead server appearing with no explanation (#206).
	if info.Mode == mcpv1alpha2.MCPServerModeContainer && info.Image == "" && templateImage(source) == "" {
		return fmt.Errorf("container mode requires an image, and neither the entry nor providerTemplate sets one")
	}

	provider := &mcpv1alpha2.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      info.Name,
			Namespace: source.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, provider, func() error {
		// Set managed-by label
		if provider.Labels == nil {
			provider.Labels = make(map[string]string)
		}
		provider.Labels[LabelDiscoveryManagedBy] = source.Name

		// Apply template labels
		if source.Spec.MCPServerTemplate != nil && source.Spec.MCPServerTemplate.Metadata != nil {
			for k, v := range source.Spec.MCPServerTemplate.Metadata.Labels {
				provider.Labels[k] = v
			}
		}

		// Apply discovery-specific labels
		for k, v := range info.Labels {
			provider.Labels[k] = v
		}

		// Apply template annotations
		if source.Spec.MCPServerTemplate != nil && source.Spec.MCPServerTemplate.Metadata != nil {
			if provider.Annotations == nil {
				provider.Annotations = make(map[string]string)
			}
			for k, v := range source.Spec.MCPServerTemplate.Metadata.Annotations {
				provider.Annotations[k] = v
			}
		}

		// Apply template spec if present
		if source.Spec.MCPServerTemplate != nil && source.Spec.MCPServerTemplate.Spec != nil {
			provider.Spec = *source.Spec.MCPServerTemplate.Spec.DeepCopy()
		}

		// Override with discovered values: the template is the default, an
		// entry that sets a field wins.
		provider.Spec.Mode = info.Mode
		if info.Endpoint != "" {
			provider.Spec.Endpoint = info.Endpoint
		}
		if info.Image != "" {
			provider.Spec.Image = info.Image
		}
		if len(info.Command) > 0 {
			provider.Spec.Command = append([]string(nil), info.Command...)
		}
		if len(info.Args) > 0 {
			provider.Spec.Args = append([]string(nil), info.Args...)
		}

		// Set controller owner reference if configured
		if source.ShouldSetController() {
			if err := controllerutil.SetControllerReference(source, provider, r.Scheme); err != nil {
				return fmt.Errorf("failed to set controller reference: %w", err)
			}
		}

		return nil
	})

	return err
}

// templateImage returns the image the source's providerTemplate sets, or "".
func templateImage(source *mcpv1alpha2.MCPDiscoverySource) string {
	if source.Spec.MCPServerTemplate == nil || source.Spec.MCPServerTemplate.Spec == nil {
		return ""
	}
	return source.Spec.MCPServerTemplate.Spec.Image
}

// authoritativeSync deletes MCPServers that are no longer discovered (scoped to successfully-scanned sources only)
func (r *MCPDiscoverySourceReconciler) authoritativeSync(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource, discovered map[string]DiscoveredMCPServerInfo) []string {
	logger := log.FromContext(ctx)
	var deleteErrors []string

	// List all MCPServers managed by this source
	providerList := &mcpv1alpha2.MCPServerList{}
	if err := r.List(ctx, providerList,
		client.InNamespace(source.Namespace),
		client.MatchingLabels{LabelDiscoveryManagedBy: source.Name},
	); err != nil {
		deleteErrors = append(deleteErrors, fmt.Sprintf("failed to list managed providers: %v", err))
		return deleteErrors
	}

	for i := range providerList.Items {
		existing := &providerList.Items[i]
		if _, found := discovered[existing.Name]; !found {
			// Provider no longer discovered -- delete it
			logger.Info("Authoritative sync: deleting provider no longer discovered", "provider", existing.Name)
			if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete provider during authoritative sync", "provider", existing.Name)
				deleteErrors = append(deleteErrors, fmt.Sprintf("delete %s: %v", existing.Name, err))
			} else {
				r.Recorder.Eventf(source, nil, corev1.EventTypeNormal, ReasonProviderGone, ActionReconcile,
					"Deleted provider %s (no longer discovered)", existing.Name)
			}
		}
	}

	return deleteErrors
}

// applyFilters applies include/exclude patterns and max provider count to discovered providers
func (r *MCPDiscoverySourceReconciler) applyFilters(source *mcpv1alpha2.MCPDiscoverySource, discovered map[string]DiscoveredMCPServerInfo) map[string]DiscoveredMCPServerInfo {
	if source.Spec.Filters == nil {
		return discovered
	}

	logger := log.Log.WithName("discovery-filter")
	filtered := make(map[string]DiscoveredMCPServerInfo)

	for name, info := range discovered {
		// Check include patterns
		if len(source.Spec.Filters.IncludePatterns) > 0 {
			matched := false
			for _, pattern := range source.Spec.Filters.IncludePatterns {
				if ok, err := regexp.MatchString(pattern, name); err == nil && ok {
					matched = true
					break
				}
			}
			if !matched {
				logger.V(1).Info("Provider excluded by include filter", "provider", name)
				continue
			}
		}

		// Check exclude patterns
		excluded := false
		for _, pattern := range source.Spec.Filters.ExcludePatterns {
			if ok, err := regexp.MatchString(pattern, name); err == nil && ok {
				excluded = true
				break
			}
		}
		if excluded {
			logger.V(1).Info("Provider excluded by exclude filter", "provider", name)
			continue
		}

		filtered[name] = info
	}

	// Apply max providers limit (deterministic: sorted by name)
	if source.Spec.Filters.MaxProviders != nil && int32(len(filtered)) > *source.Spec.Filters.MaxProviders {
		names := make([]string, 0, len(filtered))
		for name := range filtered {
			names = append(names, name)
		}
		sort.Strings(names)

		truncated := make(map[string]DiscoveredMCPServerInfo)
		for i := 0; i < int(*source.Spec.Filters.MaxProviders) && i < len(names); i++ {
			truncated[names[i]] = filtered[names[i]]
		}
		return truncated
	}

	return filtered
}

// validateDiscoveredName reports why a generated MCPServer name cannot be
// used. The name becomes the object name, a pod name suffix and the
// mcp-hangar.io/provider label value, so it must be a DNS-1123 subdomain and
// a label value (at most 63 characters).
func validateDiscoveredName(name string) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return fmt.Errorf("invalid MCPServer name: %s", strings.Join(errs, "; "))
	}
	if errs := validation.IsValidLabelValue(name); len(errs) > 0 {
		return fmt.Errorf("invalid MCPServer name: %s", strings.Join(errs, "; "))
	}
	return nil
}

// reconcileDelete handles discovery source deletion
func (r *MCPDiscoverySourceReconciler) reconcileDelete(ctx context.Context, source *mcpv1alpha2.MCPDiscoverySource) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling deletion for MCPDiscoverySource")

	// Delete the MCPServers this source manages -- unless it is Additive and
	// does not own them (ownership.controller: false). That combination says
	// the source adds servers and leaves them alone, and deleting the source
	// used to delete them anyway (#213). With an owner reference, Kubernetes
	// garbage collection removes them regardless.
	providerList := &mcpv1alpha2.MCPServerList{}
	if !source.IsAuthoritative() && !source.ShouldSetController() {
		logger.Info("Additive source without ownership: leaving its servers in place")
	} else if err := r.List(ctx, providerList,
		client.InNamespace(source.Namespace),
		client.MatchingLabels{LabelDiscoveryManagedBy: source.Name},
	); err != nil {
		logger.Error(err, "Failed to list managed providers for cleanup")
	} else {
		for i := range providerList.Items {
			existing := &providerList.Items[i]
			logger.Info("Deleting managed provider", "provider", existing.Name)
			if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete managed provider", "provider", existing.Name)
			}
		}
	}

	// Clear metrics
	metrics.ClearDiscoveryMetrics(source.Namespace, source.Name)

	// Remove finalizer
	controllerutil.RemoveFinalizer(source, finalizerName)
	if err := r.Update(ctx, source); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(source, nil, corev1.EventTypeNormal, ReasonDeleted, ActionReconcile,
		"Discovery source deleted")
	logger.Info("MCPDiscoverySource deleted successfully")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *MCPDiscoverySourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha2.MCPDiscoverySource{}).
		Owns(&mcpv1alpha2.MCPServer{}).
		Complete(r)
}

// hasRequiredAnnotations checks if all required annotations are present
func hasRequiredAnnotations(annotations map[string]string, required []string) bool {
	if annotations == nil {
		return false
	}
	for _, req := range required {
		if _, ok := annotations[req]; !ok {
			return false
		}
	}
	return true
}

// setCondition sets or updates a standard metav1.Condition on the source's
// status. v1alpha1 statuses carried their own Condition type with a
// SetCondition method; v1alpha2 uses []metav1.Condition, so the shared
// apimachinery helper does the transition-time bookkeeping.
func setCondition(source *mcpv1alpha2.MCPDiscoverySource, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&source.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: source.Generation,
	})
}
