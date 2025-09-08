// controllers/redirection_controller.go
package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	redirectorv1 "github.com/vedantaggrawal/redirectionOperator/api/v1"
	utils "github.com/vedantaggrawal/redirectionOperator/pkg/utils"
)

// RedirectionReconciler reconciles a Redirection object

type RedirectionReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

const (
	RedirectionFinalizerName = "redirector.io/finalizer"

	// Condition types
	ConditionTypeReady     = "Ready"
	ConditionTypeProcessed = "Processed"

	// Default values
	DefaultMaxHosts = 10
)

// +kubebuilder:rbac:groups=redirector.io,resources=redirections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redirector.io,resources=redirections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redirector.io,resources=redirections/finalizers,verbs=update
// +kubebuilder:rbac:groups=redirector.io,resources=domainbindings,verbs=get;list;watch;create;update;patch;delete

func (r *RedirectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("redirection", req.NamespacedName)

	// Fetch the Redirection instance
	var redirection redirectorv1.Redirection
	if err := r.Get(ctx, req.NamespacedName, &redirection); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Redirection resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Redirection")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if redirection.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &redirection)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&redirection, RedirectionFinalizerName) {
		controllerutil.AddFinalizer(&redirection, RedirectionFinalizerName)
		return ctrl.Result{}, r.Update(ctx, &redirection)
	}

	// Set default maxHosts if not specified
	maxHosts := redirection.Spec.MaxHosts
	if maxHosts <= 0 {
		maxHosts = DefaultMaxHosts
	}

	// Get previous state for change detection
	var existingBindings redirectorv1.DomainBindingList
	if err := r.List(ctx, &existingBindings, client.InNamespace(redirection.Namespace),
		client.MatchingFields{"spec.ownerRedirection": redirection.Name}); err != nil {
		log.Error(err, "Failed to list existing DomainBindings")
		return ctrl.Result{}, err
	}

	// Calculate differences
	oldSources := r.extractSourcesFromBindings(existingBindings.Items)
	newSources := redirection.Spec.Sources
	diff := utils.ComputeDifferences(oldSources, newSources)

	log.Info("Computed differences", "added", len(diff.Added), "removed", len(diff.Removed), "changed", diff.Changed)

	// Process changes
	if err := r.processChanges(ctx, &redirection, diff, maxHosts); err != nil {
		log.Error(err, "Failed to process changes")
		r.updateCondition(&redirection, ConditionTypeProcessed, metav1.ConditionFalse, "ProcessingFailed", err.Error())
		r.updateCondition(&redirection, ConditionTypeReady, metav1.ConditionFalse, "ProcessingFailed", "Failed to process domain changes")
		r.updateStatus(ctx, &redirection)
		return ctrl.Result{RequeueAfter: time.Minute * 5}, err
	}

	// Update status
	r.updateCondition(&redirection, ConditionTypeProcessed, metav1.ConditionTrue, "ProcessingCompleted", "All domain changes processed successfully")
	r.updateCondition(&redirection, ConditionTypeReady, metav1.ConditionTrue, "Ready", "Redirection is ready and active")

	redirection.Status.ParentDomains = r.extractParentDomains(newSources)
	redirection.Status.TotalSources = len(newSources)
	redirection.Status.LastUpdated = metav1.Now()

	if err := r.updateStatus(ctx, &redirection); err != nil {
		log.Error(err, "Failed to update Redirection status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *RedirectionReconciler) handleDeletion(ctx context.Context, redirection *redirectorv1.Redirection) (ctrl.Result, error) {
	log := r.Log.WithValues("redirection", redirection.Name)

	// Delete all associated DomainBindings
	var bindingList redirectorv1.DomainBindingList
	if err := r.List(ctx, &bindingList, client.InNamespace(redirection.Namespace),
		client.MatchingFields{"spec.ownerRedirection": redirection.Name}); err != nil {
		log.Error(err, "Failed to list DomainBindings for deletion")
		return ctrl.Result{}, err
	}

	for _, binding := range bindingList.Items {
		if err := r.Delete(ctx, &binding); err != nil {
			log.Error(err, "Failed to delete DomainBinding", "binding", binding.Name)
			return ctrl.Result{}, err
		}
		log.Info("Deleted DomainBinding", "binding", binding.Name)
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(redirection, RedirectionFinalizerName)
	return ctrl.Result{}, r.Update(ctx, redirection)
}

func (r *RedirectionReconciler) processChanges(ctx context.Context, redirection *redirectorv1.Redirection, diff utils.DomainDifferences, maxHosts int) error {
	log := r.Log.WithValues("redirection", redirection.Name)

	// Process each changed parent domain
	for _, parentDomain := range diff.Changed {
		bindingName := utils.GenerateDomainBindingName(parentDomain, redirection.Spec.Destination)

		// Get existing binding or create new one
		var binding redirectorv1.DomainBinding
		err := r.Get(ctx, client.ObjectKey{
			Namespace: redirection.Namespace,
			Name:      bindingName,
		}, &binding)

		isNew := false
		if apierrors.IsNotFound(err) {
			// Create new binding
			binding = redirectorv1.DomainBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bindingName,
					Namespace: redirection.Namespace,
				},
				Spec: redirectorv1.DomainBindingSpec{
					ParentDomain:     parentDomain,
					Destination:      redirection.Spec.Destination,
					MaxHosts:         maxHosts,
					Groups:           make(map[string]redirectorv1.GroupInfo),
					OwnerRedirection: redirection.Name,
				},
			}
			isNew = true
		} else if err != nil {
			return fmt.Errorf("failed to get DomainBinding %s: %w", bindingName, err)
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(redirection, &binding, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}

		// Apply changes using GroupManager
		groupManager := utils.NewGroupManager(maxHosts, binding.Spec.Groups)

		// Remove sources first
		if removed, exists := diff.Removed[parentDomain]; exists {
			log.Info("Removing sources from parent domain", "parentDomain", parentDomain, "sources", removed)
			groupManager.RemoveSources(removed)
		}

		// Add sources
		if added, exists := diff.Added[parentDomain]; exists {
			log.Info("Adding sources to parent domain", "parentDomain", parentDomain, "sources", added)
			groupManager.AddSources(added, parentDomain, redirection.Spec.Destination)
		}

		// Update binding with new groups
		binding.Spec.Groups = groupManager.Groups

		// Create or update the binding
		if isNew {
			if err := r.Create(ctx, &binding); err != nil {
				return fmt.Errorf("failed to create DomainBinding %s: %w", bindingName, err)
			}
			log.Info("Created new DomainBinding", "binding", bindingName)
		} else {
			if err := r.Update(ctx, &binding); err != nil {
				return fmt.Errorf("failed to update DomainBinding %s: %w", bindingName, err)
			}
			log.Info("Updated DomainBinding", "binding", bindingName)
		}

		// If no groups remain, delete the binding
		if len(binding.Spec.Groups) == 0 {
			if err := r.Delete(ctx, &binding); err != nil {
				return fmt.Errorf("failed to delete empty DomainBinding %s: %w", bindingName, err)
			}
			log.Info("Deleted empty DomainBinding", "binding", bindingName)
		}
	}

	return nil
}

func (r *RedirectionReconciler) extractSourcesFromBindings(bindings []redirectorv1.DomainBinding) []string {
	var sources []string
	for _, binding := range bindings {
		for _, group := range binding.Spec.Groups {
			for source := range group.Sources {
				sources = append(sources, source)
			}
		}
	}
	return sources
}

func (r *RedirectionReconciler) extractParentDomains(sources []string) []string {
	parentDomainSet := make(map[string]bool)
	for _, source := range sources {
		parent := utils.ExtractParentDomain(source)
		parentDomainSet[parent] = true
	}

	var parentDomains []string
	for parent := range parentDomainSet {
		parentDomains = append(parentDomains, parent)
	}
	return parentDomains
}

func (r *RedirectionReconciler) updateCondition(redirection *redirectorv1.Redirection, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	meta.SetStatusCondition(&redirection.Status.Conditions, condition)
}

func (r *RedirectionReconciler) updateStatus(ctx context.Context, redirection *redirectorv1.Redirection) error {
	return r.Status().Update(ctx, redirection)
}

// SetupWithManager sets up the controller with the Manager
func (r *RedirectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index DomainBindings by their owner Redirection for efficient lookups
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &redirectorv1.DomainBinding{}, "spec.ownerRedirection", func(rawObj client.Object) []string {
		binding := rawObj.(*redirectorv1.DomainBinding)
		return []string{binding.Spec.OwnerRedirection}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&redirectorv1.Redirection{}).
		Owns(&redirectorv1.DomainBinding{}).
		Complete(r)
}
