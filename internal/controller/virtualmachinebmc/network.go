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
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// A failed Multus attachment doesn't change the Deployment status, so poll.
const networkPendingRequeue = 15 * time.Second

const (
	MultusNetworkStatusAnnotation = "k8s.v1.cni.cncf.io/network-status"

	reasonNetworkAttached = "NetworkAttached"
	reasonNetworkPending  = "NetworkAttachmentPending"
	reasonNADNotFound     = "NetworkAttachmentDefinitionNotFound"
	reasonMultusMissing   = "MultusNotInstalled"
)

// multusNetworkStatus is one entry of the network-status annotation.
type multusNetworkStatus struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	MAC       string   `json:"mac,omitempty"`
}

// attachedNetwork returns nadKey's entry from a Pod created for the current
// networkRef, or nil if none has it attached yet.
func attachedNetwork(pods []corev1.Pod, networkRef, nadKey string) *bmcv1.NetworkStatus {
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil || pod.Annotations[MultusNetworksAnnotation] != networkRef {
			continue
		}
		raw, ok := pod.Annotations[MultusNetworkStatusAnnotation]
		if !ok {
			continue
		}
		var entries []multusNetworkStatus
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			continue
		}
		for _, e := range entries {
			if e.Name == nadKey {
				return &bmcv1.NetworkStatus{Name: e.Name, Interface: e.Interface, IPs: e.IPs, MAC: e.MAC}
			}
		}
	}
	return nil
}

// reconcileNetworkStatus updates status.network and the NetworkAttached
// condition. It returns true while the network is not attached yet.
func (r *VirtualMachineBMCReconciler) reconcileNetworkStatus(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) (bool, error) {
	orig := virtualMachineBMC.DeepCopy()

	nadNamespace, nadName, ok := virtualMachineBMC.Spec.NetworkRefNamespacedName(virtualMachineBMC.Namespace)
	if !ok {
		virtualMachineBMC.Status.Network = nil
		meta.RemoveStatusCondition(&virtualMachineBMC.Status.Conditions, bmcv1.ConditionNetworkAttached)
		return false, r.patchStatusIfChanged(ctx, orig, virtualMachineBMC)
	}
	nadKey := nadNamespace + "/" + nadName

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(virtualMachineBMC.Namespace), client.MatchingLabels{
		bmcv1.VirtualMachineBMCNameLabel: virtualMachineBMC.Name,
		bmcv1.VMNameLabel:                virtualMachineBMC.Spec.VirtualMachineRef.Name,
	}); err != nil {
		return false, err
	}

	condition := metav1.Condition{Type: bmcv1.ConditionNetworkAttached}
	network := attachedNetwork(pods.Items, virtualMachineBMC.Spec.NetworkRef, nadKey)
	if network != nil {
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonNetworkAttached
		condition.Message = fmt.Sprintf("Network %q is attached to the virtBMC Pod on interface %q", nadKey, network.Interface)
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason, condition.Message = r.networkPendingReason(ctx, nadNamespace, nadName)
	}

	virtualMachineBMC.Status.Network = network
	meta.SetStatusCondition(&virtualMachineBMC.Status.Conditions, condition)
	return network == nil, r.patchStatusIfChanged(ctx, orig, virtualMachineBMC)
}

func (r *VirtualMachineBMCReconciler) networkPendingReason(ctx context.Context, nadNamespace, nadName string) (string, string) {
	nadKey := nadNamespace + "/" + nadName

	nad := &unstructured.Unstructured{}
	nad.SetGroupVersionKind(bmcv1.NetworkAttachmentDefinitionGVK)
	err := r.Get(ctx, client.ObjectKey{Namespace: nadNamespace, Name: nadName}, nad)
	switch {
	case err == nil:
		return reasonNetworkPending, fmt.Sprintf("Waiting for Multus to attach network %q to the virtBMC Pod; check the Pod events if this persists", nadKey)
	case meta.IsNoMatchError(err):
		return reasonMultusMissing, "NetworkAttachmentDefinition CRD not found; Multus must be installed to use spec.networkRef"
	case apierrors.IsNotFound(err):
		return reasonNADNotFound, fmt.Sprintf("NetworkAttachmentDefinition %q not found", nadKey)
	default:
		log.FromContext(ctx).Error(err, "unable to get NetworkAttachmentDefinition", "networkAttachmentDefinition", nadKey)
		return reasonNetworkPending, fmt.Sprintf("Waiting for network %q to be attached; unable to get NetworkAttachmentDefinition: %v", nadKey, err)
	}
}

// patchStatusIfChanged uses a merge patch so other status writers aren't clobbered.
func (r *VirtualMachineBMCReconciler) patchStatusIfChanged(ctx context.Context, orig, updated *bmcv1.VirtualMachineBMC) error {
	patch := client.MergeFrom(orig)
	data, err := patch.Data(updated)
	if err != nil {
		return err
	}
	if string(data) == "{}" {
		return nil
	}
	return r.Status().Patch(ctx, updated, patch)
}
