/*
Copyright 2026.

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

package bootorderrestore

import (
	"context"
	"encoding/json"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

const (
	// bootOrderRestoreRequeueDelay is the delay before re-checking whether
	// a oneshot boot order should be restored. Used when the VMI is not yet
	// present or the UID hasn't changed (oneshot not yet consumed).
	bootOrderRestoreRequeueDelay = 10 * time.Second

	// bmcVMRefNameKey indexes VirtualMachineBMC by Spec.VirtualMachineRef.Name
	// to avoid a full-namespace List on every VMI event.
	bmcVMRefNameKey = "spec.virtualMachineRef.name"
)

// BootOrderRestoreReconciler is driven by VMI creation. It looks up VirtualMachineBMCs
// that reference the same VM and have a pending oneshot backup, then restores
// boot order / firmware / rebootPolicy when the VMI UID has changed.
type BootOrderRestoreReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch
//+kubebuilder:rbac:groups=bmc.kubevirt.io,resources=virtualmachinebmcs,verbs=get;list;watch
//+kubebuilder:rbac:groups=bmc.kubevirt.io,resources=virtualmachinebmcs/status,verbs=get;patch

// Reconcile is keyed by VirtualMachineInstance (same name as the VM). It finds
// BMCs with a pending oneshot backup for that VM and restores when consumed.
func (r *BootOrderRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bmcs, err := r.findBMCsForVM(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(bmcs) == 0 {
		return ctrl.Result{}, nil
	}

	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmiErr := r.Get(ctx, req.NamespacedName, vmi)
	if vmiErr != nil && !apierrors.IsNotFound(vmiErr) {
		return ctrl.Result{}, vmiErr
	}
	vmiMissing := apierrors.IsNotFound(vmiErr)

	needRequeue := false
	for i := range bmcs {
		bmc := &bmcs[i]
		result, err := r.reconcileBMC(ctx, bmc, vmi, vmiMissing)
		if err != nil {
			return ctrl.Result{}, err
		}
		if result.RequeueAfter > 0 {
			needRequeue = true
		}
	}

	if needRequeue {
		return ctrl.Result{RequeueAfter: bootOrderRestoreRequeueDelay}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileBMC restores oneshot state for a single BMC if the VMI UID changed.
func (r *BootOrderRestoreReconciler) reconcileBMC(
	ctx context.Context,
	bmc *bmcv1.VirtualMachineBMC,
	vmi *kubevirtv1.VirtualMachineInstance,
	vmiMissing bool,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	override := bmc.Status.BootOverride
	if override == nil {
		return ctrl.Result{}, nil
	}

	// Persistent overrides are static markers — no backup data to restore.
	// The status entry persists as state for readback (GetBootFlags / Redfish).
	if override.Mode == bmcv1.BootOverrideModePersistent {
		return ctrl.Result{}, nil
	}

	if vmiMissing {
		// If the VM itself is gone, clear the orphaned override.
		vmName := bmc.Spec.VirtualMachineRef.Name
		vm := &kubevirtv1.VirtualMachine{}
		if err := r.Get(ctx, types.NamespacedName{Name: vmName, Namespace: bmc.Namespace}, vm); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("VM not found, clearing stale oneshot override")
				_ = r.clearBootOverride(ctx, bmc)
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		log.V(1).Info("no VMI found for oneshot restore, requeuing")
		return ctrl.Result{RequeueAfter: bootOrderRestoreRequeueDelay}, nil
	}

	if string(vmi.UID) == override.VMIUID {
		log.V(1).Info("oneshot not yet consumed, VMI UID unchanged")
		return ctrl.Result{RequeueAfter: bootOrderRestoreRequeueDelay}, nil
	}

	log.Info("new VMI generation detected after oneshot was set, restoring original boot order")

	vmName := bmc.Spec.VirtualMachineRef.Name
	vm := &kubevirtv1.VirtualMachine{}
	if err := r.Get(ctx, types.NamespacedName{Name: vmName, Namespace: bmc.Namespace}, vm); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("VM not found, clearing stale oneshot override")
			_ = r.clearBootOverride(ctx, bmc)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if vm.Spec.Template == nil {
		return ctrl.Result{}, nil
	}

	vmPatchOps := resourcemanager.BuildBootOrderRestorePatch(vm, override)
	if len(vmPatchOps) > 0 {
		patchData, err := json.Marshal(vmPatchOps)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Patch(ctx, vm, client.RawPatch(types.JSONPatchType, patchData)); err != nil {
			log.Error(err, "failed to patch VM to restore boot order")
			return ctrl.Result{}, err
		}
	}

	if err := r.clearBootOverride(ctx, bmc); err != nil {
		log.Error(err, "failed to clear boot override, will retry")
		return ctrl.Result{RequeueAfter: bootOrderRestoreRequeueDelay}, nil
	}

	log.Info("oneshot boot order restored successfully")
	return ctrl.Result{}, nil
}

// clearBootOverride removes status.bootOverride from the VirtualMachineBMC CR.
// MergeFrom computes a field-level diff so only bootOverride is cleared — a
// full status update from the cached object could drop concurrently written
// fields (e.g. conditions from the virtualmachinebmc controller).
func (r *BootOrderRestoreReconciler) clearBootOverride(ctx context.Context, bmc *bmcv1.VirtualMachineBMC) error {
	orig := bmc.DeepCopy()
	bmc.Status.BootOverride = nil
	return r.Status().Patch(ctx, bmc, client.MergeFrom(orig))
}

// SetupWithManager sets up the controller with the Manager.
// Primary watch is VMI Create/Delete; BMC events with a pending backup map to the VMI name.
func (r *BootOrderRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &bmcv1.VirtualMachineBMC{}, bmcVMRefNameKey, func(rawObj client.Object) []string {
		bmc := rawObj.(*bmcv1.VirtualMachineBMC)
		if bmc.Spec.VirtualMachineRef == nil {
			return nil
		}
		return []string{bmc.Spec.VirtualMachineRef.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("bootorderrestore").
		For(&kubevirtv1.VirtualMachineInstance{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc:  func(e event.CreateEvent) bool { return true },
			UpdateFunc:  func(e event.UpdateEvent) bool { return false },
			DeleteFunc:  func(e event.DeleteEvent) bool { return true },
			GenericFunc: func(e event.GenericEvent) bool { return false },
		})).
		Watches(
			&bmcv1.VirtualMachineBMC{},
			handler.EnqueueRequestsFromMapFunc(r.findVMIForBMC),
		).
		Complete(r)
}

// findVMIForBMC maps a BMC with a pending oneshot backup to a reconcile request
// for the referenced VirtualMachineInstance (same name as the VM).
func (r *BootOrderRestoreReconciler) findVMIForBMC(_ context.Context, obj client.Object) []reconcile.Request {
	bmc, ok := obj.(*bmcv1.VirtualMachineBMC)
	if !ok {
		return nil
	}
	if bmc.Status.BootOverride == nil {
		return nil
	}
	if bmc.Spec.VirtualMachineRef == nil {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      bmc.Spec.VirtualMachineRef.Name,
			Namespace: bmc.Namespace,
		},
	}}
}

// findBMCsForVM lists BMCs in the namespace that reference vmName and have a
// pending oneshot backup in status.bootOverride.
func (r *BootOrderRestoreReconciler) findBMCsForVM(ctx context.Context, namespace, vmName string) ([]bmcv1.VirtualMachineBMC, error) {
	var vmBMCList bmcv1.VirtualMachineBMCList
	if err := r.List(ctx, &vmBMCList,
		client.InNamespace(namespace),
		client.MatchingFields{bmcVMRefNameKey: vmName},
	); err != nil {
		return nil, err
	}

	out := make([]bmcv1.VirtualMachineBMC, 0, len(vmBMCList.Items))
	for i := range vmBMCList.Items {
		bmc := &vmBMCList.Items[i]
		if bmc.Status.BootOverride == nil {
			continue
		}
		out = append(out, *bmc)
	}
	return out, nil
}
