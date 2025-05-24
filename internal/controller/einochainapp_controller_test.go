package controller

import (
	"context"
	"fmt"   // For unique naming in tests
	"time" // Added for Eventually/Consistently timeouts

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors" // For checking IsNotFound
	"k8s.io/apimachinery/pkg/api/resource" // For HPA target value
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr" // For Service TargetPort

	// "sigs.k8s.io/controller-runtime/pkg/client" // k8sClient is global from suite_test
	// "sigs.k8s.io/controller-runtime/pkg/reconcile"

	einov1alpha1 "github.com/cloudwego/eino/operator/api/v1alpha1" // Adjust import path
)

var _ = Describe("EinoChainApp Controller", func() {

	const (
		EinoChainAppNamePrefix = "test-eino-" // Use a prefix to avoid name clashes in tests
		EinoChainAppNamespace  = "default"
		TestImage              = "nginx:latest" // A real image for testing

		timeout  = time.Second * 10
		duration = time.Second * 2 // For Consistently
		interval = time.Millisecond * 250
	)

	Context("Unit tests", func() {
		Describe("getAppLabels", func() {
			It("should return the correct labels for an EinoChainApp", func() {
				einoApp := &einov1alpha1.EinoChainApp{
					ObjectMeta: metav1.ObjectMeta{
						Name:      EinoChainAppNamePrefix + "labels",
						Namespace: EinoChainAppNamespace,
					},
				}
				reconciler := &EinoChainAppReconciler{} // Client not needed for this specific unit test

				expectedLabels := map[string]string{
					"app.kubernetes.io/name":       "EinoChainApp",
					"app.kubernetes.io/instance":   EinoChainAppNamePrefix + "labels",
					"app.kubernetes.io/managed-by": "einochainapp-operator",
				}
				Expect(reconciler.getAppLabels(einoApp)).To(Equal(expectedLabels))
			})
		})
	})

	Context("Integration tests with envtest", func() {
		var testIdx int
		var einoAppName string
		var einoAppLookupKey types.NamespacedName

		BeforeEach(func() {
			// Generate a unique name for each test to avoid conflicts
			testIdx++
			einoAppName = EinoChainAppNamePrefix + fmt.Sprintf("%d", testIdx)
			einoAppLookupKey = types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}

			By("Ensuring no EinoChainApp resource exists before test: " + einoAppName)
			existingApp := &einov1alpha1.EinoChainApp{}
			err := k8sClient.Get(ctx, einoAppLookupKey, existingApp)
			if err == nil {
				// If it exists, delete it and wait
				By("Deleting pre-existing EinoChainApp for test: " + einoAppName)
				Expect(k8sClient.Delete(ctx, existingApp)).Should(Succeed())
				Eventually(func() bool {
					return errors.IsNotFound(k8sClient.Get(ctx, einoAppLookupKey, &einov1alpha1.EinoChainApp{}))
				}, timeout, interval).Should(BeTrue(), "failed to delete pre-existing EinoChainApp for test: "+einoAppName)
			} else if !errors.IsNotFound(err) {
				Fail(fmt.Sprintf("Failed to check for pre-existing EinoChainApp %s: %v", einoAppName, err))
			}
		})

		AfterEach(func() {
			By("Deleting the EinoChainApp resource after test: " + einoAppName)
			resource := &einov1alpha1.EinoChainApp{}
			err := k8sClient.Get(ctx, einoAppLookupKey, resource)
			if err == nil { // Only delete if it exists
				Expect(k8sClient.Delete(ctx, resource)).Should(Succeed())
				Eventually(func() bool {
					return errors.IsNotFound(k8sClient.Get(ctx, einoAppLookupKey, &einov1alpha1.EinoChainApp{}))
				}, timeout, interval).Should(BeTrue(), "EinoChainApp did not delete: "+einoAppName)
			} else if !errors.IsNotFound(err) {
				Fail(fmt.Sprintf("Failed to get EinoChainApp %s for cleanup: %v", einoAppName, err))
			}

			By("Ensuring owned resources are deleted for " + einoAppName)
			deploymentKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, deploymentKey, &appsv1.Deployment{}))
			}, timeout, interval).Should(BeTrue(), "Deployment not deleted for "+einoAppName)

			serviceKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, serviceKey, &corev1.Service{}))
			}, timeout, interval).Should(BeTrue(), "Service not deleted for "+einoAppName)

			hpaKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, hpaKey, &autoscalingv2.HorizontalPodAutoscaler{}))
			}, timeout, interval).Should(BeTrue(), "HPA not deleted for "+einoAppName)
		})

		Describe("EinoChainApp CRD lifecycle", func() {
			Context("When creating a new EinoChainApp CR without autoscaling", func() {
				It("Should create a Deployment and Service, and update status", func() {
					By("Creating an EinoChainApp CR without autoscaling: " + einoAppName)
					initialReplicas := int32(2)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{
							Name:      einoAppName,
							Namespace: EinoChainAppNamespace,
						},
						Spec: einov1alpha1.EinoChainAppSpec{
							Image:    TestImage,
							Replicas: &initialReplicas,
							ServiceSpec: &corev1.ServiceSpec{ 
								Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080)}},
								Type:  corev1.ServiceTypeClusterIP,
							},
						},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying Deployment creation for " + einoAppName)
					createdDeployment := &appsv1.Deployment{}
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdDeployment)
					}, timeout, interval).Should(Succeed())
					Expect(*createdDeployment.Spec.Replicas).Should(Equal(initialReplicas))
					Expect(createdDeployment.Spec.Template.Spec.Containers[0].Image).Should(Equal(TestImage))

					By("Verifying Service creation for " + einoAppName)
					createdService := &corev1.Service{}
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdService)
					}, timeout, interval).Should(Succeed())
					Expect(createdService.Spec.Ports[0].Port).Should(Equal(int32(80)))
					Expect(createdService.Spec.Type).Should(Equal(corev1.ServiceTypeClusterIP))

					By("Verifying HPA is not created for " + einoAppName)
					Consistently(func() bool {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, &autoscalingv2.HorizontalPodAutoscaler{})
						return errors.IsNotFound(err)
					}, duration, interval).Should(BeTrue())

					By("Verifying EinoChainApp status for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Eventually(func() int32 {
						err := k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)
						if err != nil {
							return -1 
						}
						return updatedEinoApp.Status.DesiredReplicas
					}, timeout, interval).Should(Equal(initialReplicas))
					
					Eventually(func(g Gomega) {
						err := k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)
						g.Expect(err).NotTo(HaveOccurred())
						// Example: Check for Available condition, might be False initially as pods are not ready
						g.Expect(updatedEinoApp.Status.Conditions).To(ContainElement(And(
							HaveField("Type", ConditionTypeAvailable),
							// Status might be False initially, then True. For this test, we check it's set.
						)))
						g.Expect(updatedEinoApp.Status.Conditions).To(ContainElement(And(
							HaveField("Type", ConditionTypeProgressing), 
							// Status might be True initially as deployment is progressing
						)))
					}, timeout, interval).Should(Succeed())
				})
			})

			Context("When creating a new EinoChainApp CR with autoscaling", func() {
				It("Should create a Deployment, Service, HPA, and update status", func() {
					By("Creating an EinoChainApp CR with autoscaling: " + einoAppName)
					minReplicas := int32(1)
					maxReplicas := int32(3)
					targetTPS := int32(100)
					initialDeploymentReplicas := int32(1) // Default initial replicas for Deployment when HPA is active

					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{
							Name:      einoAppName,
							Namespace: EinoChainAppNamespace,
						},
						Spec: einov1alpha1.EinoChainAppSpec{
							Image: TestImage,
							// Replicas field should be set by the reconciler to MinReplicas if HPA is active,
							// or to a default like 1 if not specified by user.
							Replicas: &initialDeploymentReplicas, // reconciler will use this if HPA is off, HPA overrides
							ServiceSpec: &corev1.ServiceSpec{
								Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080)}},
								Type:  corev1.ServiceTypeClusterIP,
							},
							Autoscaling: &einov1alpha1.EinoChainAppAutoscalingSpec{
								MinReplicas:       &minReplicas,
								MaxReplicas:       maxReplicas,
								TargetTokenPerSec: &targetTPS,
							},
						},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying Deployment creation for " + einoAppName)
					createdDeployment := &appsv1.Deployment{}
					Eventually(func(g Gomega) {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdDeployment)
						g.Expect(err).NotTo(HaveOccurred())
						// The reconciler sets Deployment replicas to MinReplicas (or a default if MinReplicas is not set, which is 1 here)
						// when HPA is active.
						g.Expect(*createdDeployment.Spec.Replicas).Should(Equal(initialDeploymentReplicas)) 
					}, timeout, interval).Should(Succeed())


					By("Verifying Service creation for " + einoAppName)
					createdService := &corev1.Service{}
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdService)
					}, timeout, interval).Should(Succeed())
					Expect(createdService.Spec.Ports[0].Port).Should(Equal(int32(80)))

					By("Verifying HPA creation for " + einoAppName)
					createdHPA := &autoscalingv2.HorizontalPodAutoscaler{}
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdHPA)
					}, timeout, interval).Should(Succeed())
					Expect(*createdHPA.Spec.MinReplicas).Should(Equal(minReplicas))
					Expect(createdHPA.Spec.MaxReplicas).Should(Equal(maxReplicas))
					Expect(createdHPA.Spec.Metrics).Should(HaveLen(1))
					Expect(createdHPA.Spec.Metrics[0].Type).Should(Equal(autoscalingv2.PodsMetricSourceType))
					Expect(createdHPA.Spec.Metrics[0].Pods.Metric.Name).Should(Equal("eino_token_per_sec_avg_per_pod"))
					Expect(createdHPA.Spec.Metrics[0].Pods.Target.Type).Should(Equal(autoscalingv2.AverageValueMetricType))
					Expect(createdHPA.Spec.Metrics[0].Pods.Target.AverageValue.Value()).Should(Equal(resource.NewQuantity(targetTPS, resource.DecimalSI).Value()))


					By("Verifying EinoChainApp status for HPA for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Eventually(func(g Gomega) {
						err := k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(updatedEinoApp.Status.HPAStatus).NotTo(BeNil())
						if updatedEinoApp.Status.HPAStatus != nil { // Guard against nil pointer
						    // HPA controller sets desired replicas. Initially, it might be minReplicas.
							g.Expect(updatedEinoApp.Status.HPAStatus.DesiredReplicas).Should(Equal(minReplicas))
						}
						g.Expect(updatedEinoApp.Status.Conditions).To(ContainElement(And(
							HaveField("Type", ConditionTypeAutoscalingActive),
							// Status might be False initially if HPA is still initializing, then True if active.
							// For this test, we primarily care that the condition is set.
						)))
					}, timeout, interval).Should(Succeed())
				})
			})

			Context("When updating an EinoChainApp CR", func() {
				It("Should update the image in the Deployment", func() {
					By("Creating an EinoChainApp CR: " + einoAppName)
					initialImage := TestImage // e.g., "nginx:latest"
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec:       einov1alpha1.EinoChainAppSpec{Image: initialImage},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying Deployment is created with initial image for " + einoAppName)
					createdDeployment := &appsv1.Deployment{}
					Eventually(func() string {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdDeployment)
						if err != nil {
							return ""
						}
						if len(createdDeployment.Spec.Template.Spec.Containers) > 0 {
							return createdDeployment.Spec.Template.Spec.Containers[0].Image
						}
						return ""
					}, timeout, interval).Should(Equal(initialImage))

					By("Updating the EinoChainApp CR with a new image for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Expect(k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)).Should(Succeed())

					newImage := "nginx:1.21-alpine"
					updatedEinoApp.Spec.Image = newImage
					Expect(k8sClient.Update(ctx, updatedEinoApp)).Should(Succeed())

					By("Verifying Deployment is updated with the new image for " + einoAppName)
					Eventually(func() string {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdDeployment)
						if err != nil {
							return ""
						}
						if len(createdDeployment.Spec.Template.Spec.Containers) > 0 {
							return createdDeployment.Spec.Template.Spec.Containers[0].Image
						}
						return ""
					}, timeout, interval).Should(Equal(newImage))
				})

				It("Should update replicas if HPA is not active", func() {
					By("Creating an EinoChainApp CR without autoscaling: " + einoAppName)
					initialReplicas := int32(1)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec:       einov1alpha1.EinoChainAppSpec{Image: TestImage, Replicas: &initialReplicas},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying Deployment is created with initial replicas for " + einoAppName)
					createdDeployment := &appsv1.Deployment{}
					Eventually(func() int32 {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdDeployment)
						if err != nil || createdDeployment.Spec.Replicas == nil {
							return -1 // Invalid state
						}
						return *createdDeployment.Spec.Replicas
					}, timeout, interval).Should(Equal(initialReplicas))

					By("Updating the EinoChainApp CR with new replica count for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Expect(k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)).Should(Succeed())
					
					newReplicas := int32(3)
					updatedEinoApp.Spec.Replicas = &newReplicas
					Expect(k8sClient.Update(ctx, updatedEinoApp)).Should(Succeed())

					By("Verifying Deployment is updated with new replicas for " + einoAppName)
					Eventually(func() int32 {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdDeployment)
						if err != nil || createdDeployment.Spec.Replicas == nil {
							return -1
						}
						return *createdDeployment.Spec.Replicas
					}, timeout, interval).Should(Equal(newReplicas))
				})

				It("Should update Service ports", func() {
					By("Creating an EinoChainApp CR with a ServiceSpec: " + einoAppName)
					initialPort := int32(80)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec: einov1alpha1.EinoChainAppSpec{
							Image: TestImage,
							ServiceSpec: &corev1.ServiceSpec{
								Ports: []corev1.ServicePort{{Name: "http", Port: initialPort, TargetPort: intstr.FromInt(8080)}},
								Type:  corev1.ServiceTypeClusterIP,
							},
						},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying Service is created with initial port for " + einoAppName)
					createdService := &corev1.Service{}
					Eventually(func() int32 {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdService)
						if err != nil || len(createdService.Spec.Ports) == 0 {
							return -1
						}
						return createdService.Spec.Ports[0].Port
					}, timeout, interval).Should(Equal(initialPort))

					By("Updating the EinoChainApp CR with new Service port for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Expect(k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)).Should(Succeed())

					newPort := int32(8081)
					updatedEinoApp.Spec.ServiceSpec.Ports[0].Port = newPort
					Expect(k8sClient.Update(ctx, updatedEinoApp)).Should(Succeed())

					By("Verifying Service is updated with the new port for " + einoAppName)
					Eventually(func() int32 {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdService)
						if err != nil || len(createdService.Spec.Ports) == 0 {
							return -1
						}
						return createdService.Spec.Ports[0].Port
					}, timeout, interval).Should(Equal(newPort))
				})
				
				It("Should create HPA when autoscaling is enabled (from no HPA)", func() {
					By("Creating an EinoChainApp CR without autoscaling: " + einoAppName)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec:       einov1alpha1.EinoChainAppSpec{Image: TestImage},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying HPA is not created initially for " + einoAppName)
					Consistently(func() bool {
						return errors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, &autoscalingv2.HorizontalPodAutoscaler{}))
					}, duration, interval).Should(BeTrue())

					By("Updating the EinoChainApp CR to enable autoscaling for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Expect(k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)).Should(Succeed())

					minReplicas := int32(1)
					maxReplicas := int32(5)
					targetTPS := int32(50)
					updatedEinoApp.Spec.Autoscaling = &einov1alpha1.EinoChainAppAutoscalingSpec{
						MinReplicas:       &minReplicas,
						MaxReplicas:       maxReplicas,
						TargetTokenPerSec: &targetTPS,
					}
					Expect(k8sClient.Update(ctx, updatedEinoApp)).Should(Succeed())

					By("Verifying HPA is created after update for " + einoAppName)
					createdHPA := &autoscalingv2.HorizontalPodAutoscaler{}
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdHPA)
					}, timeout, interval).Should(Succeed())
					Expect(*createdHPA.Spec.MinReplicas).Should(Equal(minReplicas))
					Expect(createdHPA.Spec.MaxReplicas).Should(Equal(maxReplicas))
				})

				It("Should update HPA spec when autoscaling parameters change", func() {
					By("Creating an EinoChainApp CR with autoscaling: " + einoAppName)
					initialMinReplicas := int32(1)
					initialMaxReplicas := int32(3)
					initialTargetTPS := int32(100)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec: einov1alpha1.EinoChainAppSpec{
							Image: TestImage,
							Autoscaling: &einov1alpha1.EinoChainAppAutoscalingSpec{
								MinReplicas:       &initialMinReplicas,
								MaxReplicas:       initialMaxReplicas,
								TargetTokenPerSec: &initialTargetTPS,
							},
						},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying HPA is created with initial spec for " + einoAppName)
					createdHPA := &autoscalingv2.HorizontalPodAutoscaler{}
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdHPA)
					}, timeout, interval).Should(Succeed())
					Expect(*createdHPA.Spec.MinReplicas).Should(Equal(initialMinReplicas))

					By("Updating the EinoChainApp CR with new HPA parameters for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Expect(k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)).Should(Succeed())

					newMinReplicas := int32(2)
					newTargetTPS := int32(150)
					updatedEinoApp.Spec.Autoscaling.MinReplicas = &newMinReplicas
					updatedEinoApp.Spec.Autoscaling.TargetTokenPerSec = &newTargetTPS
					Expect(k8sClient.Update(ctx, updatedEinoApp)).Should(Succeed())

					By("Verifying HPA spec is updated for " + einoAppName)
					Eventually(func(g Gomega) {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, createdHPA)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(*createdHPA.Spec.MinReplicas).Should(Equal(newMinReplicas))
						g.Expect(createdHPA.Spec.Metrics[0].Pods.Target.AverageValue.Value()).Should(Equal(resource.NewQuantity(newTargetTPS, resource.DecimalSI).Value()))
					}, timeout, interval).Should(Succeed())
				})

				It("Should delete HPA when autoscaling is disabled", func() {
					By("Creating an EinoChainApp CR with autoscaling: " + einoAppName)
					minReplicas := int32(1)
					maxReplicas := int32(3)
					targetTPS := int32(100)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec: einov1alpha1.EinoChainAppSpec{
							Image: TestImage,
							Autoscaling: &einov1alpha1.EinoChainAppAutoscalingSpec{
								MinReplicas:       &minReplicas,
								MaxReplicas:       maxReplicas,
								TargetTokenPerSec: &targetTPS,
							},
						},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					By("Verifying HPA is created for " + einoAppName)
					Eventually(func() error {
						return k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, &autoscalingv2.HorizontalPodAutoscaler{})
					}, timeout, interval).Should(Succeed())

					By("Updating the EinoChainApp CR to disable autoscaling for " + einoAppName)
					updatedEinoApp := &einov1alpha1.EinoChainApp{}
					Expect(k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)).Should(Succeed())
					
					updatedEinoApp.Spec.Autoscaling = nil // Disable autoscaling
					Expect(k8sClient.Update(ctx, updatedEinoApp)).Should(Succeed())

					By("Verifying HPA is deleted for " + einoAppName)
					Eventually(func() bool {
						return errors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}, &autoscalingv2.HorizontalPodAutoscaler{}))
					}, timeout, interval).Should(BeTrue())
				})
			})

			Context("When deleting an EinoChainApp CR", func() {
				It("Should garbage collect owned Deployment, Service, and HPA", func() {
					By("Creating an EinoChainApp CR with service and HPA for deletion test: " + einoAppName)
					initialReplicas := int32(1)
					minReplicas := int32(1)
					maxReplicas := int32(2)
					targetTPS := int32(100)
					einoApp := &einov1alpha1.EinoChainApp{
						ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
						Spec: einov1alpha1.EinoChainAppSpec{
							Image:    TestImage,
							Replicas: &initialReplicas, // Will be managed by HPA if active
							ServiceSpec: &corev1.ServiceSpec{
								Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(8080)}},
								Type:  corev1.ServiceTypeClusterIP,
							},
							Autoscaling: &einov1alpha1.EinoChainAppAutoscalingSpec{
								MinReplicas:       &minReplicas,
								MaxReplicas:       maxReplicas,
								TargetTokenPerSec: &targetTPS,
							},
						},
					}
					Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

					deploymentKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
					serviceKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
					hpaKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}

					By("Verifying Deployment, Service, and HPA are created for deletion test: " + einoAppName)
					Eventually(func() error { return k8sClient.Get(ctx, deploymentKey, &appsv1.Deployment{}) }, timeout, interval).Should(Succeed())
					Eventually(func() error { return k8sClient.Get(ctx, serviceKey, &corev1.Service{}) }, timeout, interval).Should(Succeed())
					Eventually(func() error { return k8sClient.Get(ctx, hpaKey, &autoscalingv2.HorizontalPodAutoscaler{}) }, timeout, interval).Should(Succeed())

					By("Deleting the EinoChainApp CR for deletion test: " + einoAppName)
					Expect(k8sClient.Delete(ctx, einoApp)).Should(Succeed())

					By("Verifying EinoChainApp CR is deleted for deletion test: " + einoAppName)
					Eventually(func() bool {
						return errors.IsNotFound(k8sClient.Get(ctx, einoAppLookupKey, &einov1alpha1.EinoChainApp{}))
					}, timeout, interval).Should(BeTrue())
					
					By("Verifying owned Deployment is garbage collected for " + einoAppName)
					Eventually(func() bool { return errors.IsNotFound(k8sClient.Get(ctx, deploymentKey, &appsv1.Deployment{})) }, timeout, interval).Should(BeTrue())
					By("Verifying owned Service is garbage collected for " + einoAppName)
					Eventually(func() bool { return errors.IsNotFound(k8sClient.Get(ctx, serviceKey, &corev1.Service{})) }, timeout, interval).Should(BeTrue())
					By("Verifying owned HPA is garbage collected for " + einoAppName)
					Eventually(func() bool { return errors.IsNotFound(k8sClient.Get(ctx, hpaKey, &autoscalingv2.HorizontalPodAutoscaler{})) }, timeout, interval).Should(BeTrue())
				})
			})
		})

		Describe("Status updates", func() {
			It("Should reflect Deployment readiness in EinoChainApp status", func() {
				By("Creating an EinoChainApp for status test: " + einoAppName)
				initialReplicas := int32(1)
				einoApp := &einov1alpha1.EinoChainApp{
					ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
					Spec:       einov1alpha1.EinoChainAppSpec{Image: TestImage, Replicas: &initialReplicas},
				}
				Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

				deploymentKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
				createdDeployment := &appsv1.Deployment{}
				By("Waiting for Deployment to be created for status test: " + einoAppName)
				Eventually(func() error {
					return k8sClient.Get(ctx, deploymentKey, createdDeployment)
				}, timeout, interval).Should(Succeed())

				By("Manually updating Deployment status to simulate readiness for " + einoAppName)
				// Simulate the deployment controller updating the status
				createdDeployment.Status = appsv1.DeploymentStatus{
					Replicas:           initialReplicas,
					ReadyReplicas:      initialReplicas,
					AvailableReplicas:  initialReplicas,
					UpdatedReplicas:    initialReplicas,
					ObservedGeneration: createdDeployment.Generation, // Crucial for Progressing condition
				}
				Expect(k8sClient.Status().Update(ctx, createdDeployment)).Should(Succeed())

				By("Verifying EinoChainApp status reflects Deployment readiness for " + einoAppName)
				updatedEinoApp := &einov1alpha1.EinoChainApp{}
				Eventually(func(g Gomega) {
					err := k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)
					g.Expect(err).NotTo(HaveOccurred())
					
					g.Expect(updatedEinoApp.Status.CurrentReplicas).Should(Equal(initialReplicas))
					g.Expect(updatedEinoApp.Status.DesiredReplicas).Should(Equal(initialReplicas)) // Assuming no HPA

					g.Expect(updatedEinoApp.Status.Conditions).To(ContainElement(And(
						HaveField("Type", ConditionTypeAvailable),
						HaveField("Status", metav1.ConditionTrue),
						HaveField("Reason", ReasonMinimumReplicasAvailable), // Or ReasonDeploymentReady
					)))
					g.Expect(updatedEinoApp.Status.Conditions).To(ContainElement(And(
						HaveField("Type", ConditionTypeProgressing),
						HaveField("Status", metav1.ConditionFalse), // Should be false if stable
						HaveField("Reason", ReasonDeploymentReady),
					)))
				}, timeout, interval).Should(Succeed())
			})

			It("Should reflect HPA status in EinoChainApp status", func() {
				By("Creating an EinoChainApp with autoscaling for HPA status test: " + einoAppName)
				minReplicas := int32(1)
				maxReplicas := int32(3)
				targetTPS := int32(100)
				initialDeploymentReplicas := int32(1)

				einoApp := &einov1alpha1.EinoChainApp{
					ObjectMeta: metav1.ObjectMeta{Name: einoAppName, Namespace: EinoChainAppNamespace},
					Spec: einov1alpha1.EinoChainAppSpec{
						Image:    TestImage,
						Replicas: &initialDeploymentReplicas, // Initial deployment replicas
						Autoscaling: &einov1alpha1.EinoChainAppAutoscalingSpec{
							MinReplicas:       &minReplicas,
							MaxReplicas:       maxReplicas,
							TargetTokenPerSec: &targetTPS,
						},
					},
				}
				Expect(k8sClient.Create(ctx, einoApp)).Should(Succeed())

				hpaKey := types.NamespacedName{Name: einoAppName, Namespace: EinoChainAppNamespace}
				createdHPA := &autoscalingv2.HorizontalPodAutoscaler{}
				By("Verifying HPA is created for HPA status test: " + einoAppName)
				Eventually(func() error {
					return k8sClient.Get(ctx, hpaKey, createdHPA)
				}, timeout, interval).Should(Succeed())

				By("Manually updating HPA status for test: " + einoAppName)
				// Simulate HPA controller updating status
				createdHPA.Status = autoscalingv2.HorizontalPodAutoscalerStatus{
					CurrentReplicas:    2, // Example current replicas
					DesiredReplicas:    2, // Example desired replicas by HPA
					LastScaleTime:      &metav1.Time{Time: time.Now().Add(-time.Minute)},
					ObservedGeneration: createdHPA.Generation, // HPA controller sets this
					CurrentMetrics: []autoscalingv2.MetricStatus{
						{
							Type: autoscalingv2.PodsMetricSourceType,
							Pods: &autoscalingv2.PodsMetricStatus{
								Metric:  autoscalingv2.MetricIdentifier{Name: "eino_token_per_sec_avg_per_pod"},
								Current: autoscalingv2.MetricValueStatus{AverageValue: resource.NewQuantity(int64(120), resource.DecimalSI)},
							},
						},
					},
					Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
						{Type: autoscalingv2.AbleToScale, Status: metav1.ConditionTrue, Reason: "ReadyForNewScale", Message: "recommended size matches current size"},
						{Type: autoscalingv2.ScalingActive, Status: metav1.ConditionTrue, Reason: "ValidMetricFound", Message: "the HPA was able to successfully calculate a replica count from pods metric eino_token_per_sec_avg_per_pod"},
					},
				}
				Expect(k8sClient.Status().Update(ctx, createdHPA)).Should(Succeed())

				By("Verifying EinoChainApp status reflects HPA status for " + einoAppName)
				updatedEinoApp := &einov1alpha1.EinoChainApp{}
				Eventually(func(g Gomega) {
					err := k8sClient.Get(ctx, einoAppLookupKey, updatedEinoApp)
					g.Expect(err).NotTo(HaveOccurred())
					
					g.Expect(updatedEinoApp.Status.HPAStatus).NotTo(BeNil())
					if updatedEinoApp.Status.HPAStatus != nil {
						g.Expect(updatedEinoApp.Status.HPAStatus.CurrentReplicas).Should(Equal(int32(2)))
						g.Expect(updatedEinoApp.Status.HPAStatus.DesiredReplicas).Should(Equal(int32(2))) // This should come from HPA
						g.Expect(updatedEinoApp.Status.ObservedTokenPerSec).NotTo(BeNil())
						if updatedEinoApp.Status.ObservedTokenPerSec != nil {
							g.Expect(*updatedEinoApp.Status.ObservedTokenPerSec).Should(Equal(int32(120)))
						}
					}
					// Check overall EinoChainApp DesiredReplicas is updated by HPA's status
					g.Expect(updatedEinoApp.Status.DesiredReplicas).Should(Equal(int32(2)))


					g.Expect(updatedEinoApp.Status.Conditions).To(ContainElement(And(
						HaveField("Type", ConditionTypeAutoscalingActive),
						HaveField("Status", metav1.ConditionTrue),
						// HaveField("Reason", ReasonHPAActive), // This can vary based on HPA internal state
					)))
				}, timeout, interval).Should(Succeed())
			})
		})
	})
})
```
