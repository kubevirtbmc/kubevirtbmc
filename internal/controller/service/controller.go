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

package service

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// ServiceReconciler reconciles a Service object
type ServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

type ServiceStatus struct {
	Ready   bool
	Message string
}

func (s *ServiceReconciler) checkServiceReadiness(ctx context.Context, svc *corev1.Service, virtualMachineBMC *bmcv1.VirtualMachineBMC) (ServiceStatus, corev1.ServiceType) {
	log := log.FromContext(ctx)
	serviceType := corev1.ServiceTypeClusterIP
	if virtualMachineBMC.Spec.Service != nil && virtualMachineBMC.Spec.Service.Type != "" {
		serviceType = virtualMachineBMC.Spec.Service.Type
	}
	virtualMachineBMC.Status.LoadBalancerIP = ""

	var status ServiceStatus

	switch serviceType {
	case corev1.ServiceTypeLoadBalancer:
		virtualMachineBMC.Status.ClusterIP = svc.Spec.ClusterIP
		status.Ready = len(svc.Status.LoadBalancer.Ingress) > 0 && svc.Status.LoadBalancer.Ingress[0].IP != ""
		if status.Ready {
			virtualMachineBMC.Status.LoadBalancerIP = svc.Status.LoadBalancer.Ingress[0].IP
			status.Message = "LoadBalancer IP assigned to the Service"

		} else {
			status.Message = "LoadBalancer IP not yet assigned to the Service"
		}
	case corev1.ServiceTypeNodePort:
		status.Ready = len(svc.Spec.Ports) > 0 && svc.Spec.Ports[0].NodePort > 0
		if status.Ready {
			virtualMachineBMC.Status.ClusterIP = svc.Spec.ClusterIP
			status.Message = "NodePort assigned to the Service"

		} else {
			status.Message = "NodePort not yet assigned to the Service"
		}
	case corev1.ServiceTypeClusterIP:
		status.Ready = svc.Spec.ClusterIP != ""
		if status.Ready {
			virtualMachineBMC.Status.ClusterIP = svc.Spec.ClusterIP
			status.Message = "ClusterIP assigned to the Service"
		} else {
			status.Message = "ClusterIP not yet assigned to the Service"
		}

	default:
		log.Error(fmt.Errorf("unsupported service type %s", serviceType), "unsupported Service type for VirtualMachineBMC", "serviceType", serviceType)
		status.Ready = false
		status.Message = fmt.Sprintf("unsupported service type: %s", serviceType)
	}

	return status, serviceType
}

//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups=virtualmachine.kubevirt.io,resources=virtualmachinebmcs,verbs=get;list;watch
//+kubebuilder:rbac:groups=virtualmachine.kubevirt.io,resources=virtualmachinebmcs/status,verbs=get;update;patch

func (s *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var svc corev1.Service
	if err := s.Get(ctx, req.NamespacedName, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch Service")
		return ctrl.Result{}, err
	}

	if svc.Labels == nil {
		return ctrl.Result{}, nil
	}
	virtualMachineBMCName, ok := svc.Labels[bmcv1.VirtualMachineBMCNameLabel]
	if !ok {
		return ctrl.Result{}, nil
	}
	virtualMachineBMCNamespacedName := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      virtualMachineBMCName,
	}

	var virtualMachineBMC bmcv1.VirtualMachineBMC
	if err := s.Get(ctx, virtualMachineBMCNamespacedName, &virtualMachineBMC); err != nil {
		log.Error(err, "unable to fetch VirtualMachineBMC")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	prevClusterIP := virtualMachineBMC.Status.ClusterIP
	prevLoadBalancerIP := virtualMachineBMC.Status.LoadBalancerIP

	status, svcType := s.checkServiceReadiness(ctx, &svc, &virtualMachineBMC)

	condition := metav1.Condition{
		Type:    bmcv1.ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  bmcv1.ConditionNotReady,
		Message: status.Message,
	}
	if status.Ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = bmcv1.ConditionReady
	}

	conditionChanged := meta.SetStatusCondition(&virtualMachineBMC.Status.Conditions, condition)
	ipChanged := virtualMachineBMC.Status.ClusterIP != prevClusterIP ||
		virtualMachineBMC.Status.LoadBalancerIP != prevLoadBalancerIP

	if conditionChanged || ipChanged {
		if err := s.Status().Update(ctx, &virtualMachineBMC); err != nil {
			log.Error(err, "unable to update VirtualMachineBMC status for Service")
			return ctrl.Result{}, err
		}
		log.V(1).Info("updated VirtualMachineBMC status for Service", "virtualMachineBMC", virtualMachineBMC)
	}

	if !status.Ready {
		log.V(1).Info(
			"Service not ready yet", "serviceType", svcType, "service", svc.Name,
		)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (s *ServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Complete(s)
}
