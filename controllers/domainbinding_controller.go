// controllers/domainbinding_controller.go
package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	redirectorv1 "github.com/vedantaggrawal/redirectionOperator/api/v1"
	metrics "github.com/vedantaggrawal/redirectionOperator/metrics"
	utils "github.com/vedantaggrawal/redirectionOperator/pkg/utils"
)

// DomainBindingReconciler reconciles a DomainBinding object
type DomainBindingReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

const (
	DomainBindingFinalizerName = "redirector.io/domainbinding-finalizer"

	// Default service configuration
	DefaultServiceName = "redirector-service"
	DefaultServicePort = 80

	// Ingress annotations
	AnnotationCertManager = "cert-manager.io/cluster-issuer"
	DefaultClusterIssuer  = "letsencrypt-prod"

	// Status reasons
	ReasonIngressCreated = "IngressCreated"
	ReasonIngressUpdated = "IngressUpdated"
	ReasonIngressDeleted = "IngressDeleted"
	ReasonIngressFailed  = "IngressFailed"
)

// +kubebuilder:rbac:groups=redirector.io,resources=domainbindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redirector.io,resources=domainbindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redirector.io,resources=domainbindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

func (r *DomainBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("domainbinding", req.NamespacedName)

	// Fetch the DomainBinding instance
	var binding redirectorv1.DomainBinding
	if err := r.Get(ctx, req.NamespacedName, &binding); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("DomainBinding resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get DomainBinding")
		return ctrl.Result{}, err
	}

	metrics.GroupsPerParentDomainTotal.WithLabelValues(binding.Spec.ParentDomain, binding.Spec.Destination).Set(float64(binding.Status.TotalGroups))

	metrics.SourcesPerParentDomainTotal.WithLabelValues(binding.Spec.ParentDomain, binding.Spec.Destination).Set(float64(binding.Status.TotalSources))

	metrics.SourcePerGroupUpdatesTotal.WithLabelValues(binding.Spec.ParentDomain, binding.Spec.Destination).Inc()

	metrics.ParentDomainUpdatesTotal.WithLabelValues(binding.Spec.ParentDomain, binding.Spec.Destination).Inc()

	// Handle deletion
	if binding.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &binding)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&binding, DomainBindingFinalizerName) {
		controllerutil.AddFinalizer(&binding, DomainBindingFinalizerName)
		return ctrl.Result{}, r.Update(ctx, &binding)
	}

	// Generate Ingress name
	ingressName := utils.GenerateIngressName(binding.Spec.ParentDomain, binding.Spec.Destination)

	// Check if we need to create/update the Ingress
	var ingress networkingv1.Ingress
	err := r.Get(ctx, client.ObjectKey{
		Namespace: binding.Namespace,
		Name:      ingressName,
	}, &ingress)

	if apierrors.IsNotFound(err) {
		// Create new Ingress
		if err := r.createIngress(ctx, &binding, ingressName); err != nil {
			log.Error(err, "Failed to create Ingress")
			r.updateCondition(&binding, ConditionTypeReady, metav1.ConditionFalse, ReasonIngressFailed, err.Error())
			r.updateStatus(ctx, &binding)
			return ctrl.Result{RequeueAfter: time.Minute * 2}, err
		}

		r.updateCondition(&binding, ConditionTypeReady, metav1.ConditionTrue, ReasonIngressCreated, "Ingress created successfully")
		binding.Status.IngressName = ingressName

	} else if err != nil {
		log.Error(err, "Failed to get Ingress")
		return ctrl.Result{}, err
	} else {
		// Update existing Ingress
		if r.shouldUpdateIngress(&ingress, &binding) {
			if err := r.updateIngress(ctx, &ingress, &binding); err != nil {
				log.Error(err, "Failed to update Ingress")
				r.updateCondition(&binding, ConditionTypeReady, metav1.ConditionFalse, ReasonIngressFailed, err.Error())
				r.updateStatus(ctx, &binding)
				return ctrl.Result{RequeueAfter: time.Minute * 2}, err
			}

			r.updateCondition(&binding, ConditionTypeReady, metav1.ConditionTrue, ReasonIngressUpdated, "Ingress updated successfully")
		}

		binding.Status.IngressName = ingressName
	}

	// Update status
	r.updateBindingStatus(&binding)
	if err := r.updateStatus(ctx, &binding); err != nil {
		log.Error(err, "Failed to update DomainBinding status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled DomainBinding", "ingress", ingressName, "groups", len(binding.Spec.Groups))
	return ctrl.Result{}, nil
}

func (r *DomainBindingReconciler) handleDeletion(ctx context.Context, binding *redirectorv1.DomainBinding) (ctrl.Result, error) {
	log := r.Log.WithValues("domainbinding", binding.Name)

	// Delete associated Ingress
	ingressName := utils.GenerateIngressName(binding.Spec.ParentDomain, binding.Spec.Destination)
	var ingress networkingv1.Ingress
	err := r.Get(ctx, client.ObjectKey{
		Namespace: binding.Namespace,
		Name:      ingressName,
	}, &ingress)

	if err == nil {
		if err := r.Delete(ctx, &ingress); err != nil {
			log.Error(err, "Failed to delete Ingress", "ingress", ingressName)
			return ctrl.Result{}, err
		}
		log.Info("Deleted Ingress", "ingress", ingressName)
	} else if !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to get Ingress for deletion", "ingress", ingressName)
		return ctrl.Result{}, err
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(binding, DomainBindingFinalizerName)
	return ctrl.Result{}, r.Update(ctx, binding)
}

func (r *DomainBindingReconciler) createIngress(ctx context.Context, binding *redirectorv1.DomainBinding, ingressName string) error {
	ingress := r.buildIngress(binding, ingressName)

	// Set owner reference
	if err := controllerutil.SetControllerReference(binding, ingress, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	return r.Create(ctx, ingress)
}

func (r *DomainBindingReconciler) updateIngress(ctx context.Context, ingress *networkingv1.Ingress, binding *redirectorv1.DomainBinding) error {
	newIngress := r.buildIngress(binding, ingress.Name)

	// Preserve existing metadata
	//newIngress.ObjectMeta = ingress.ObjectMeta
	newIngress.ResourceVersion = ingress.ResourceVersion

	return r.Update(ctx, newIngress)
}

func (r *DomainBindingReconciler) buildIngress(binding *redirectorv1.DomainBinding, ingressName string) *networkingv1.Ingress {
	var allSources []string
	sourceSet := make(map[string]struct{})
	var configSnippets []string
	var serverSnippets []string

	for _, group := range binding.Spec.Groups {
		for source := range group.Sources {
			allSources = append(allSources, source)
			sourceSet[source] = struct{}{}
		}
	}
	sort.Strings(allSources)

	// Build TLS configuration (unchanged)
	var tlsConfig []networkingv1.IngressTLS
	for groupIndex, group := range binding.Spec.Groups {
		var hosts []string
		for source := range group.Sources {
			hosts = append(hosts, source)
		}
		sort.Strings(hosts)
		if len(hosts) > 0 {
			tlsConfig = append(tlsConfig, networkingv1.IngressTLS{
				Hosts:      hosts,
				SecretName: group.SecretName,
			})
		}
		if binding.Status.CertificateStatus == nil {
			binding.Status.CertificateStatus = make(map[string]string)
		}
		binding.Status.CertificateStatus[groupIndex] = "Requested"
	}
	sort.Slice(tlsConfig, func(i, j int) bool {
		return tlsConfig[i].SecretName < tlsConfig[j].SecretName
	})

	// Build rules for all sources (unchanged)
	var rules []networkingv1.IngressRule
	pathType := networkingv1.PathTypePrefix
	for _, source := range allSources {
		rules = append(rules, networkingv1.IngressRule{
			Host: source,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: DefaultServiceName,
									Port: networkingv1.ServiceBackendPort{
										Number: DefaultServicePort,
									},
								},
							},
						},
					},
				},
			},
		})

		// Configuration snippet: HTTPS redirect logic
		if strings.HasPrefix(source, "www.") {
			rest := strings.TrimPrefix(source, "www.")
			// Redirect www.domain → destination
			configSnippets = append(configSnippets,
				fmt.Sprintf(`if ($host = "%s") { return 301 https://%s$request_uri; }`,
					rest, source),
			)
		}
		// Naked → destination (if no www exists)
		configSnippets = append(configSnippets,
			fmt.Sprintf(`if ($host = "%s") { return 301 https://%s$request_uri; }`,
				source, binding.Spec.Destination),
		)

		// Server snippet: HTTP redirect logic
		serverSnippets = append(serverSnippets, fmt.Sprintf(`if ($scheme = http) {if ($host = "%s") {return 301 https://%s$request_uri;}
}`, source, source))
	}

	annotations := map[string]string{
		AnnotationCertManager: DefaultClusterIssuer,
	}

	// Add configuration-snippet if needed
	if len(configSnippets) > 0 {
		annotations["nginx.ingress.kubernetes.io/configuration-snippet"] =
			`more_set_headers "X-Content-Type-Options: nosniff";` + "\n" + strings.Join(configSnippets, "\n")
	}

	// Add server-snippet if needed
	if len(serverSnippets) > 0 {
		annotations["nginx.ingress.kubernetes.io/server-snippet"] = strings.Join(serverSnippets, "\n")
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        ingressName,
			Namespace:   binding.Namespace,
			Annotations: annotations,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "domain-redirector",
				"app.kubernetes.io/component":  "ingress",
				"app.kubernetes.io/managed-by": "domain-redirector-operator",
				"redirector.io/parent-domain":  binding.Spec.ParentDomain,
				"redirector.io/destination":    binding.Spec.Destination,
			},
		},
		Spec: networkingv1.IngressSpec{
			TLS:   tlsConfig,
			Rules: rules,
		},
	}
}

func (r *DomainBindingReconciler) shouldUpdateIngress(ingress *networkingv1.Ingress, binding *redirectorv1.DomainBinding) bool {
	// Simple check: compare number of rules and TLS entries
	expectedSources := 0
	expectedTLS := len(binding.Spec.Groups)

	for _, group := range binding.Spec.Groups {
		expectedSources += len(group.Sources)
	}

	return len(ingress.Spec.Rules) != expectedSources || len(ingress.Spec.TLS) != expectedTLS
}

func (r *DomainBindingReconciler) updateBindingStatus(binding *redirectorv1.DomainBinding) {
	binding.Status.TotalGroups = len(binding.Spec.Groups)

	totalSources := 0
	for _, group := range binding.Spec.Groups {
		totalSources += group.Counter
	}
	binding.Status.TotalSources = totalSources
	binding.Status.LastReconciled = metav1.Now()
}

func (r *DomainBindingReconciler) updateCondition(binding *redirectorv1.DomainBinding, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	meta.SetStatusCondition(&binding.Status.Conditions, condition)
}

func (r *DomainBindingReconciler) updateStatus(ctx context.Context, binding *redirectorv1.DomainBinding) error {
	return r.Status().Update(ctx, binding)
}

// SetupWithManager sets up the controller with the Manager
func (r *DomainBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redirectorv1.DomainBinding{}).
		Owns(&networkingv1.Ingress{}).
		Complete(r)
}
