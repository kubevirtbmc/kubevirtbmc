package virtbmc

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

const bootOverrideWatchRetryInterval = time.Second

// runBootOverrideReconcile is shared by managed and standalone agents. It
// sleeps while no override is active, then watches only the target VM and VMI.
func (b *VirtBMC) runBootOverrideReconcile(active bool) {
	for {
		if !active {
			select {
			case <-b.context.Done():
				return
			case <-b.resourceManager.BootOverrideChanges():
			}
			var err error
			active, err = b.resourceManager.ReconcileBootOverride(b.context)
			if err != nil {
				active, err = b.retryBootOverrideReconcile(err)
				if err != nil {
					return
				}
			}
			continue
		}

		var err error
		active, err = b.watchActiveBootOverride()
		if b.context.Err() != nil {
			return
		}
		if err != nil {
			active, err = b.retryBootOverrideReconcile(err)
			if err != nil {
				return
			}
		}
	}
}

func (b *VirtBMC) watchActiveBootOverride() (bool, error) {
	vmWatch, vmiWatch, err := b.resourceManager.WatchBootOverrideSources(b.context)
	if err != nil {
		return true, err
	}
	defer vmWatch.Stop()
	defer vmiWatch.Stop()

	// The watches are established before this read so an event cannot be lost
	// between the last reconcile and waiting on ResultChan.
	active, err := b.resourceManager.ReconcileBootOverride(b.context)
	if err != nil || !active {
		return active, err
	}

	// The reconcile's triggers are identity-only: it acts on VM/VMI existence
	// and UID changes, reading live objects when it runs. Same-UID Modified
	// events — status heartbeats, the bulk of watch traffic — cannot change
	// its outcome, so they are skipped; a recreated object coalesced into a
	// single Modified event still carries a new UID and reconciles.
	var lastVMUID, lastVMIUID types.UID

	for {
		reconcile := true
		select {
		case <-b.context.Done():
			return false, b.context.Err()
		case <-b.resourceManager.BootOverrideChanges():
		case event, ok := <-vmWatch.ResultChan():
			if !ok {
				return true, nil
			}
			if event.Type == watch.Error {
				return true, fmt.Errorf("VM watch returned an error event")
			}
			if vm, ok := event.Object.(*kubevirtv1.VirtualMachine); ok {
				reconcile = event.Type != watch.Modified || vm.UID != lastVMUID
				lastVMUID = vm.UID
			}
		case event, ok := <-vmiWatch.ResultChan():
			if !ok {
				return true, nil
			}
			if event.Type == watch.Error {
				return true, fmt.Errorf("VMI watch returned an error event")
			}
			if vmi, ok := event.Object.(*kubevirtv1.VirtualMachineInstance); ok {
				reconcile = event.Type != watch.Modified || vmi.UID != lastVMIUID
				lastVMIUID = vmi.UID
			}
		}

		if !reconcile {
			continue
		}

		active, err = b.resourceManager.ReconcileBootOverride(b.context)
		if err != nil {
			return true, err
		}
		if !active {
			return false, nil
		}
	}
}

func (b *VirtBMC) retryBootOverrideReconcile(reconcileErr error) (bool, error) {
	for {
		logrus.WithError(reconcileErr).Warn("failed to reconcile boot override")
		timer := time.NewTimer(bootOverrideWatchRetryInterval)
		select {
		case <-b.context.Done():
			timer.Stop()
			return false, b.context.Err()
		case <-b.resourceManager.BootOverrideChanges():
			timer.Stop()
		case <-timer.C:
		}

		active, err := b.resourceManager.ReconcileBootOverride(b.context)
		if err == nil {
			return active, nil
		}
		reconcileErr = err
	}
}
