/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package virtualmachinebmc

import (
	"context"
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// VirtualMachineBMCReconciler reconciles a VirtualMachineBMC object
type VirtualMachineBMCReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	AgentImageName     string
	AgentImageTag      string
	AgentCPURequest    string
	AgentMemoryRequest string
}

const (
	clusterRoleName = "kubevirtbmc-virtbmc-role"
	bmcUserKey      = "username"
	bmcPasswordKey  = "password"
)

var (
	ownerKey = ".metadata.controller"
	apiGVStr = bmcv1.GroupVersion.String()
)

func (r *VirtualMachineBMCReconciler) constructServiceAccount(virtualMachineBMC *bmcv1.VirtualMachineBMC) *corev1.ServiceAccount {
	serviceAccountName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: virtualMachineBMC.Namespace,
		},
	}
	return sa
}

func (r *VirtualMachineBMCReconciler) constructRoleBinding(virtualMachineBMC *bmcv1.VirtualMachineBMC) *rbacv1.RoleBinding {
	serviceAccountName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)
	roleBindingName := fmt.Sprintf("%s-virtbmc-rolebinding", virtualMachineBMC.Spec.VirtualMachineRef.Name)
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: virtualMachineBMC.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      serviceAccountName,
				Namespace: virtualMachineBMC.Namespace,
			},
		},
	}
	return rb
}

func (r *VirtualMachineBMCReconciler) ensureRBACResources(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) error {
	log := log.FromContext(ctx)

	sa := r.constructServiceAccount(virtualMachineBMC)

	if err := ctrl.SetControllerReference(virtualMachineBMC, sa, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, sa); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "unable to create ServiceAccount", "serviceAccount", sa.Name)
			return err
		}
		log.V(1).Info("ServiceAccount already exists", "serviceAccount", sa.Name)
	} else {
		log.V(1).Info("created ServiceAccount", "serviceAccount", sa.Name)
	}

	rb := r.constructRoleBinding(virtualMachineBMC)

	if err := ctrl.SetControllerReference(virtualMachineBMC, rb, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, rb); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "unable to create RoleBinding", "roleBinding", rb.Name)
			return err
		}
		log.V(1).Info("RoleBinding already exists", "roleBinding", rb.Name)
	} else {
		log.V(1).Info("created RoleBinding", "roleBinding", rb.Name)
	}

	return nil
}

// agentResourceRequests returns the resource requests for the virtBMC agent
// container, falling back to the defaults when not configured. Values are
// validated at controller startup, so MustParse is safe here.
func (r *VirtualMachineBMCReconciler) agentResourceRequests() corev1.ResourceList {
	cpuRequest, memoryRequest := r.AgentCPURequest, r.AgentMemoryRequest
	if cpuRequest == "" {
		cpuRequest = DefaultAgentCPURequest
	}
	if memoryRequest == "" {
		memoryRequest = DefaultAgentMemoryRequest
	}
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpuRequest),
		corev1.ResourceMemory: resource.MustParse(memoryRequest),
	}
}

func (r *VirtualMachineBMCReconciler) constructServiceFromVirtualMachineBMC(virtualMachineBMC *bmcv1.VirtualMachineBMC) *corev1.Service {
	name := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)

	labels := map[string]string{
		bmcv1.VirtualMachineBMCNameLabel: virtualMachineBMC.Name,
		bmcv1.VMNameLabel:                virtualMachineBMC.Spec.VirtualMachineRef.Name,
	}

	annotations := map[string]string{}

	if virtualMachineBMC.Spec.Service != nil {
		if virtualMachineBMC.Spec.Service.Labels != nil {
			for k, v := range virtualMachineBMC.Spec.Service.Labels {
				labels[k] = v
			}
		}

		if virtualMachineBMC.Spec.Service.Annotations != nil {
			for k, v := range virtualMachineBMC.Spec.Service.Annotations {
				annotations[k] = v
			}
		}
	}

	svcType := corev1.ServiceTypeClusterIP
	if virtualMachineBMC.Spec.Service != nil && virtualMachineBMC.Spec.Service.Type != "" {
		svcType = virtualMachineBMC.Spec.Service.Type
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      labels,
			Annotations: annotations,
			Name:        name,
			Namespace:   virtualMachineBMC.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type: svcType,
			Selector: map[string]string{
				bmcv1.VirtualMachineBMCNameLabel: virtualMachineBMC.Name,
			},
			Ports: func() []corev1.ServicePort {
				ports := []corev1.ServicePort{
					{
						Name:       redfishPortName,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromString(redfishPortName),
						Port:       RedfishSvcPort,
					},
				}
				if specIPMIEnabled(&virtualMachineBMC.Spec) {
					ports = append(ports, corev1.ServicePort{
						Name:       ipmiPortName,
						Protocol:   corev1.ProtocolUDP,
						TargetPort: intstr.FromString(ipmiPortName),
						Port:       IPMISvcPort,
					})
				}
				return ports
			}(),
		},
	}

	return svc
}

// patchStatusCondition records the given condition on the VirtualMachineBMC
// status. It uses a merge patch computed from the pre-mutation object rather
// than a full status update, so a status field written by another actor (the
// virtbmc pod writing status.bootOverride) is never clobbered by a stale copy.
func (r *VirtualMachineBMCReconciler) patchStatusCondition(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC, condition metav1.Condition) error {
	orig := virtualMachineBMC.DeepCopy()
	if !meta.SetStatusCondition(&virtualMachineBMC.Status.Conditions, condition) {
		return nil
	}
	return r.Status().Patch(ctx, virtualMachineBMC, client.MergeFrom(orig))
}

func (r *VirtualMachineBMCReconciler) validateVirtualMachineExists(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) (bool, error) {
	log := log.FromContext(ctx)

	vmKey := client.ObjectKey{
		Namespace: virtualMachineBMC.Namespace,
		Name:      virtualMachineBMC.Spec.VirtualMachineRef.Name,
	}

	var vm kubevirtv1.VirtualMachine
	if err := r.Get(ctx, vmKey, &vm); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Referenced VirtualMachine not found",
				"vm", virtualMachineBMC.Spec.VirtualMachineRef.Name,
				"namespace", virtualMachineBMC.Namespace)

			return false, r.patchStatusCondition(ctx, virtualMachineBMC, metav1.Condition{
				Type:   bmcv1.ConditionVirtualMachineAvailable,
				Status: metav1.ConditionFalse,
				Reason: "VirtualMachineNotFound",
				Message: fmt.Sprintf("VirtualMachine %q not found in namespace %q",
					virtualMachineBMC.Spec.VirtualMachineRef.Name,
					virtualMachineBMC.Namespace),
			})
		}
		log.Error(err, "error checking VirtualMachine existence")
		return false, err
	}

	if err := r.patchStatusCondition(ctx, virtualMachineBMC, metav1.Condition{
		Type:    bmcv1.ConditionVirtualMachineAvailable,
		Status:  metav1.ConditionTrue,
		Reason:  "VirtualMachineFound",
		Message: fmt.Sprintf("VirtualMachine %q is available", virtualMachineBMC.Spec.VirtualMachineRef.Name),
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (r *VirtualMachineBMCReconciler) validateSecretExists(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) (bool, error) {
	log := log.FromContext(ctx)

	if virtualMachineBMC.Spec.AuthSecretRef == nil {
		log.Info("AuthSecretRef is not set, skipping secret validation")
		return false, nil
	}

	secretKey := client.ObjectKey{
		Namespace: virtualMachineBMC.Namespace,
		Name:      virtualMachineBMC.Spec.AuthSecretRef.Name,
	}

	var secret corev1.Secret
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Referenced Secret not found",
				"secret", virtualMachineBMC.Spec.AuthSecretRef.Name,
				"namespace", virtualMachineBMC.Namespace)

			return false, r.patchStatusCondition(ctx, virtualMachineBMC, metav1.Condition{
				Type:   bmcv1.ConditionSecretAvailable,
				Status: metav1.ConditionFalse,
				Reason: "SecretNotFound",
				Message: fmt.Sprintf("Secret %q not found in namespace %q",
					virtualMachineBMC.Spec.AuthSecretRef.Name,
					virtualMachineBMC.Namespace),
			})
		}
		log.Error(err, "error checking Secret existence")
		return false, err
	}

	if err := r.patchStatusCondition(ctx, virtualMachineBMC, metav1.Condition{
		Type:    bmcv1.ConditionSecretAvailable,
		Status:  metav1.ConditionTrue,
		Reason:  "SecretFound",
		Message: fmt.Sprintf("Secret %q is available", virtualMachineBMC.Spec.AuthSecretRef.Name),
	}); err != nil {
		return false, err
	}
	return true, nil
}

func specIPMIEnabled(spec *bmcv1.VirtualMachineBMCSpec) bool {
	return spec.IPMI != nil && spec.IPMI.Enabled != nil && *spec.IPMI.Enabled
}

func (r *VirtualMachineBMCReconciler) patchVirtBMCServicePorts(
	ctx context.Context,
	virtualMachineBMC *bmcv1.VirtualMachineBMC,
) error {
	log := log.FromContext(ctx)
	svcName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)

	existing := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      svcName,
		Namespace: virtualMachineBMC.Namespace,
	}, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	desired := r.constructServiceFromVirtualMachineBMC(virtualMachineBMC)

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec.Ports = desired.Spec.Ports

	if err := r.Patch(ctx, existing, patch); err != nil {
		log.Error(err, "unable to patch Service ports", "svc", svcName)
		return err
	}

	log.V(1).Info("patched Service ports in-place", "svc", svcName,
		"ports", existing.Spec.Ports)
	return nil
}

func (r *VirtualMachineBMCReconciler) deleteVirtBMCService(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) error {
	log := log.FromContext(ctx)
	serviceName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: virtualMachineBMC.Namespace,
		},
	}

	if err := r.Delete(ctx, service); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("virtBMC Service already absent", "service", serviceName)
			return nil
		}

		log.Error(err, "unable to delete virtBMC Service", "service", serviceName)
		return err
	}

	return nil
}

func (r *VirtualMachineBMCReconciler) desiredServiceType(vmbmc *bmcv1.VirtualMachineBMC) corev1.ServiceType {
	if vmbmc.Spec.Service == nil {
		return corev1.ServiceTypeClusterIP
	}
	if vmbmc.Spec.Service.Type == "" {
		return corev1.ServiceTypeClusterIP
	}
	return vmbmc.Spec.Service.Type
}

func (r *VirtualMachineBMCReconciler) reconcileService(ctx context.Context, vmbmc *bmcv1.VirtualMachineBMC, svc *corev1.Service) error {
	log := log.FromContext(ctx)

	desiredType := r.desiredServiceType(vmbmc)
	serviceName := fmt.Sprintf("%s-virtbmc", vmbmc.Spec.VirtualMachineRef.Name)
	existingService := &corev1.Service{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: vmbmc.Namespace,
		Name:      serviceName,
	}, existingService); err != nil {
		if apierrors.IsNotFound(err) {
			svc.Spec.Type = desiredType
			if err := r.Create(ctx, svc); err != nil {
				log.Error(err, "unable to create Service for VirtualMachineBMC")
				return err
			}
			log.V(1).Info("created Service for VirtualMachineBMC", "type", desiredType)
			return nil
		}
		log.Error(err, "unable to fetch Service")
		return err
	}
	if existingService.Spec.Type != desiredType {
		log.Info("Service type changed, deleting Service",
			"oldType", existingService.Spec.Type,
			"newType", desiredType,
		)
		return r.deleteVirtBMCService(ctx, vmbmc)
	}

	if !maps.Equal(existingService.Labels, svc.Labels) || !maps.Equal(existingService.Annotations, svc.Annotations) {
		log.Info("Service labels/annotations changed, patching Service in-place", "service", serviceName)
		patch := client.MergeFrom(existingService.DeepCopy())
		existingService.Labels = svc.Labels
		existingService.Annotations = svc.Annotations
		if err := r.Patch(ctx, existingService, patch); err != nil {
			log.Error(err, "unable to patch Service labels/annotations", "service", serviceName)
			return err
		}
	}

	return nil
}

//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch;delete
//+kubebuilder:rbac:groups=bmc.kubevirt.io,resources=virtualmachinebmcs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=bmc.kubevirt.io,resources=virtualmachinebmcs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=bmc.kubevirt.io,resources=virtualmachinebmcs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VirtualMachineBMC object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *VirtualMachineBMCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var virtualMachineBMC bmcv1.VirtualMachineBMC
	if err := r.Get(ctx, req.NamespacedName, &virtualMachineBMC); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch VirtualMachineBMC")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	vmExists, err := r.validateVirtualMachineExists(ctx, &virtualMachineBMC)
	if err != nil {
		return ctrl.Result{}, err
	}

	secretExists, err := r.validateSecretExists(ctx, &virtualMachineBMC)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !vmExists || !secretExists {
		if err := r.deleteVirtBMCDeployment(ctx, &virtualMachineBMC); err != nil {
			log.Error(err, "unable to delete virtBMC Deployment")
			return ctrl.Result{}, err
		}
	}

	if virtualMachineBMC.Spec.VirtualMachineRef == nil || !vmExists || virtualMachineBMC.Spec.AuthSecretRef == nil || !secretExists {
		log.Info("Prerequisites not met, skipping reconciling",
			"vmExists", vmExists,
			"secretExists", secretExists,
			"authSecretRefSet", virtualMachineBMC.Spec.AuthSecretRef != nil)
		return ctrl.Result{}, nil
	}

	if err := r.patchVirtBMCServicePorts(ctx, &virtualMachineBMC); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureRBACResources(ctx, &virtualMachineBMC); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.deleteLegacyVirtBMCPod(ctx, &virtualMachineBMC); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureVirtBMCDeployment(ctx, &virtualMachineBMC); err != nil {
		return ctrl.Result{}, err
	}

	svc := r.constructServiceFromVirtualMachineBMC(&virtualMachineBMC)
	if err := ctrl.SetControllerReference(&virtualMachineBMC, svc, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileService(ctx, &virtualMachineBMC, svc); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineBMCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &appsv1.Deployment{}, ownerKey, func(rawObj client.Object) []string {
		// grab the deployment object, extract the owner...
		deployment := rawObj.(*appsv1.Deployment)
		owner := metav1.GetControllerOf(deployment)
		if owner == nil {
			return nil
		}
		// ...make sure it's a VirtualMachineBMC...
		if owner.APIVersion != apiGVStr || owner.Kind != "VirtualMachineBMC" {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Service{}, ownerKey, func(rawObj client.Object) []string {
		// grab the svc object, extract the owner...
		svc := rawObj.(*corev1.Service)
		owner := metav1.GetControllerOf(svc)
		if owner == nil {
			return nil
		}
		// ...make sure it's a VirtualMachineBMC...
		if owner.APIVersion != apiGVStr || owner.Kind != "VirtualMachineBMC" {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&bmcv1.VirtualMachineBMC{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&kubevirtv1.VirtualMachine{},
			handler.EnqueueRequestsFromMapFunc(r.findVirtualMachineBMCsForSecretAndVM),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findVirtualMachineBMCsForSecretAndVM),
		).
		Complete(r)
}

func (r *VirtualMachineBMCReconciler) findVirtualMachineBMCsForSecretAndVM(ctx context.Context, obj client.Object) []reconcile.Request {
	log := log.FromContext(ctx)

	var vmBMCList bmcv1.VirtualMachineBMCList
	if err := r.List(ctx, &vmBMCList, client.InNamespace(obj.GetNamespace())); err != nil {
		log.Error(err, "unable to list VirtualMachineBMCs")
		return nil
	}

	var requests []reconcile.Request

	for _, vmBMC := range vmBMCList.Items {
		match := false

		switch o := obj.(type) {
		case *kubevirtv1.VirtualMachine:
			if vmBMC.Spec.VirtualMachineRef != nil && vmBMC.Spec.VirtualMachineRef.Name == o.GetName() {
				match = true
			}

		case *corev1.Secret:
			if vmBMC.Spec.AuthSecretRef != nil && vmBMC.Spec.AuthSecretRef.Name == o.GetName() {
				match = true
			}
		}

		if match {
			log.Info("Enqueueing VirtualMachineBMC",
				"changedObject", obj.GetName(),
				"vmBMC", vmBMC.Name)

			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmBMC.Name,
					Namespace: vmBMC.Namespace,
				},
			})
		}
	}

	return requests
}
