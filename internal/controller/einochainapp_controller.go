package controller

import (
	"context"
	"context"
	"fmt"
	"reflect" // For deep comparison

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource" // Required for HPA metric values
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	// "k8s.io/apimachinery/pkg/util/intstr" // Not directly used in HPA but often nearby
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	einov1alpha1 "github.com/cloudwego/eino/operator/api/v1alpha1"
)

// EinoChainAppReconciler reconciles a EinoChainApp object
type EinoChainAppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Recorder record.EventRecorder // Uncomment if event recording is needed
}

//+kubebuilder:rbac:groups=eino.cloudwego.io,resources=einochainapps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=eino.cloudwego.io,resources=einochainapps/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=eino.cloudwego.io,resources=einochainapps/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch // If event recording is used

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *EinoChainAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling EinoChainApp")

	einoApp := &einov1alpha1.EinoChainApp{}
	if err := r.Get(ctx, req.NamespacedName, einoApp); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("EinoChainApp resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get EinoChainApp")
		return ctrl.Result{}, err
	}

	// Handle finalizers if needed (example)
	// myFinalizerName := "eino.cloudwego.io/finalizer"
	// if einoApp.ObjectMeta.DeletionTimestamp.IsZero() {
	// 	if !controllerutil.ContainsFinalizer(einoApp, myFinalizerName) {
	// 		controllerutil.AddFinalizer(einoApp, myFinalizerName)
	// 		if err := r.Update(ctx, einoApp); err != nil {
	// 			return ctrl.Result{}, err
	// 		}
	// 	}
	// } else {
	// 	if controllerutil.ContainsFinalizer(einoApp, myFinalizerName) {
	// 		// if err := r.deleteExternalResources(ctx, einoApp); err != nil {
	// 		// 	return ctrl.Result{}, err
	// 		// }
	// 		controllerutil.RemoveFinalizer(einoApp, myFinalizerName)
	// 		if err := r.Update(ctx, einoApp); err != nil {
	// 			return ctrl.Result{}, err
	// 		}
	// 	}
	// 	return ctrl.Result{}, nil
	// }
	
	// Reconcile Deployment
	if err := r.reconcileDeployment(ctx, einoApp); err != nil {
		logger.Error(err, "Failed to reconcile Deployment")
		// Consider setting a status condition here
		return ctrl.Result{}, err
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, einoApp); err != nil {
		logger.Error(err, "Failed to reconcile Service")
		// Consider setting a status condition here
		return ctrl.Result{}, err
	}

	// Placeholder for HPA, and Status updates

	logger.Info("Successfully reconciled EinoChainApp", "Name", einoApp.Name)
	return ctrl.Result{}, nil
}

func (r *EinoChainAppReconciler) reconcileDeployment(ctx context.Context, einoApp *einov1alpha1.EinoChainApp) error {
	logger := log.FromContext(ctx)
	deploymentName := einoApp.Name
	namespace := einoApp.Namespace

	desiredReplicas := int32(1) // Default replicas
	if einoApp.Spec.Replicas != nil {
		desiredReplicas = *einoApp.Spec.Replicas
	}
	// If HPA is enabled, HPA will manage replicas. For initial creation, we use spec.replicas or default.

	// Define the desired Deployment object
	// Start with the DeploymentTemplate as a base for the PodTemplateSpec
	podTemplateSpec := einoApp.Spec.DeploymentTemplate.DeepCopy()

	// Ensure labels from getAppLabels are on the pod template metadata, adding/overwriting
	if podTemplateSpec.ObjectMeta.Labels == nil {
		podTemplateSpec.ObjectMeta.Labels = make(map[string]string)
	}
	for k, v := range r.getAppLabels(einoApp) {
		podTemplateSpec.ObjectMeta.Labels[k] = v
	}

	// Ensure the primary container is correctly defined from spec.Image
	// If DeploymentTemplate.Spec.Containers is empty, create a default one.
	// If not empty, assume the first container is the primary and set its image and default name if necessary.
	if len(podTemplateSpec.Spec.Containers) == 0 {
		podTemplateSpec.Spec.Containers = make([]corev1.Container, 1)
	}
	podTemplateSpec.Spec.Containers[0].Image = einoApp.Spec.Image
	if podTemplateSpec.Spec.Containers[0].Name == "" {
		podTemplateSpec.Spec.Containers[0].Name = "eino-chain-app" // Default container name
	}
	// Other fields from einoApp.Spec.DeploymentTemplate.Spec.Containers[0] like Ports, Env, Resources, VolumeMounts
	// are preserved if they were set in the DeploymentTemplate.
	// Other PodSpec fields from DeploymentTemplate (e.g. Volumes, ServiceAccountName) are also preserved due to DeepCopy.


	desiredDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
			Labels:    r.getAppLabels(einoApp), // Labels for the Deployment resource itself
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desiredReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: r.getAppLabels(einoApp), // Selector must match pod labels
			},
			Template: *podTemplateSpec, // Use the merged pod template spec
		},
	}

	if err := controllerutil.SetControllerReference(einoApp, desiredDeployment, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Deployment: %w", err)
	}

	// Check if the Deployment already exists
	existingDeployment := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: namespace}, existingDeployment)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Creating a new Deployment", "Deployment.Namespace", namespace, "Deployment.Name", deploymentName)
			if errCreate := r.Create(ctx, desiredDeployment); errCreate != nil {
				return fmt.Errorf("failed to create new Deployment: %w", errCreate)
			}
			logger.Info("Deployment created successfully")
			return nil // Deployment created
		}
		return fmt.Errorf("failed to get existing Deployment: %w", err)
	}

	// Deployment exists, check for updates.
	updateNeeded := false

	// Check 1: Image change in the primary container
	if len(existingDeployment.Spec.Template.Spec.Containers) == 0 || // Should not happen if we created it
		existingDeployment.Spec.Template.Spec.Containers[0].Image != desiredDeployment.Spec.Template.Spec.Containers[0].Image {
		logger.Info("Deployment image changed", "current", existingDeployment.Spec.Template.Spec.Containers[0].Image, "desired", desiredDeployment.Spec.Template.Spec.Containers[0].Image)
		updateNeeded = true
	}

	// Check 2: Replicas change (only if HPA is not active)
	if einoApp.Spec.Autoscaling == nil { // If HPA is active, it controls the replicas
		if existingDeployment.Spec.Replicas == nil || *existingDeployment.Spec.Replicas != *desiredDeployment.Spec.Replicas {
			currentReplicasStr := "nil"
			if existingDeployment.Spec.Replicas != nil {
				currentReplicasStr = fmt.Sprint(*existingDeployment.Spec.Replicas)
			}
			logger.Info("Deployment replica count changed (no HPA)", "current", currentReplicasStr, "desired", *desiredDeployment.Spec.Replicas)
			updateNeeded = true
		}
	}
	
	// Check 3: PodTemplateSpec changes (more comprehensively)
	// A common way is to compare a hash of the template, or use a more sophisticated comparison.
	// For now, we'll do a basic check on a few fields. This should be made more robust.
	// We compare the desired template (which includes merged user input) with the existing one.
	// Note: Kubernetes might add default values to the existing template, so direct DeepEqual can be tricky.
	
	// Example: Compare primary container name (if it was customized and changed)
	if len(existingDeployment.Spec.Template.Spec.Containers) > 0 && len(desiredDeployment.Spec.Template.Spec.Containers) > 0 &&
		existingDeployment.Spec.Template.Spec.Containers[0].Name != desiredDeployment.Spec.Template.Spec.Containers[0].Name {
		logger.Info("Deployment primary container name changed", "current", existingDeployment.Spec.Template.Spec.Containers[0].Name, "desired", desiredDeployment.Spec.Template.Spec.Containers[0].Name)
		updateNeeded = true
	}
	// TODO: Add more comparisons for other critical fields in DeploymentTemplate like Env, Resources, Volumes etc.
	// For example: if !reflect.DeepEqual(existingDeployment.Spec.Template.Spec.Containers[0].Env, desiredDeployment.Spec.Template.Spec.Containers[0].Env) { updateNeeded = true }

	if updateNeeded {
		logger.Info("Updating existing Deployment", "Deployment.Namespace", namespace, "Deployment.Name", deploymentName)
		
		updatedDeployment := existingDeployment.DeepCopy() // Start with the existing one
		updatedDeployment.Spec.Template = desiredDeployment.Spec.Template // Apply the new template
		if einoApp.Spec.Autoscaling == nil { // Only set replicas if HPA is not managing it
			updatedDeployment.Spec.Replicas = desiredDeployment.Spec.Replicas
		}
		// If HPA is active, HPA will manage .Spec.Replicas.
		// We set it on create, but HPA takes over. On update, we only adjust if HPA is off.

		if errUpdate := r.Update(ctx, updatedDeployment); errUpdate != nil {
			return fmt.Errorf("failed to update Deployment: %w", errUpdate)
		}
		logger.Info("Deployment updated successfully")
	} else {
		logger.Info("No significant update needed for Deployment based on current checks", "Deployment.Namespace", namespace, "Deployment.Name", deploymentName)
	}

	return nil
}

// getAppLabels returns the labels for selecting the resources
// belonging to the given EinoChainApp CR name.
func (r *EinoChainAppReconciler) getAppLabels(einoApp *einov1alpha1.EinoChainApp) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "EinoChainApp",
		"app.kubernetes.io/instance":   einoApp.Name,
		"app.kubernetes.io/managed-by": "einochainapp-operator", // Or your operator name
		// Consider adding eino.cloudwego.io/einochainapp: einoApp.Name
	}
}


// SetupWithManager sets up the controller with the Manager.
func (r *EinoChainAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// r.Recorder = mgr.GetEventRecorderFor("einochainapp-controller") // Uncomment if event recording is used
	return ctrl.NewControllerManagedBy(mgr).
		For(&einov1alpha1.EinoChainApp{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}).
		Complete(r)
}

// Note: The import path "github.com/cloudwego/eino/operator/api/v1alpha1" is a placeholder.
// It should be adjusted to the actual module path of the operator project if it's different.
// For example, if the operator is in its own repository `github.com/example/eino-operator`,
// then the path would be `github.com/example/eino-operator/api/v1alpha1`.
// The worker should assume this path is correct or use a generic placeholder if creating a new project.
// The finalizer logic is commented out for now to keep the initial implementation simpler.
// It can be added later if needed for graceful deletion or external resource cleanup.
// The merge logic for deploymentTemplate is improved but can be made more robust.
// The update logic for the deployment is also improved but can be made more robust.
