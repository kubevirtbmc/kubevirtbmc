package virtbmc

import (
	"encoding/json"
	"time"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// oneshotRestorePollInterval is how often the standalone agent checks whether
// a pending oneshot override has been consumed. The check short-circuits on
// the in-memory state store, so the apiserver is only touched while a oneshot
// is actually pending.
const oneshotRestorePollInterval = 5 * time.Second

// runOneshotRestore replaces the bootorderrestore controller in standalone
// mode: it polls the VMI and restores the backed-up boot order once the
// oneshot override has been consumed (detected via VMI UID change).
func (b *VirtBMC) runOneshotRestore() {
	ticker := time.NewTicker(oneshotRestorePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.context.Done():
			return
		case <-ticker.C:
			b.restoreConsumedOneshot()
		}
	}
}

func (b *VirtBMC) restoreConsumedOneshot() {
	override, err := b.resourceManager.GetBootOverride()
	if err != nil {
		logrus.WithError(err).Warn("failed to read boot override state")
		return
	}
	if override == nil || override.Mode != bmcv1.BootOverrideModeOneshot {
		return
	}

	vmi, err := b.virtClient.KubevirtV1().VirtualMachineInstances(b.vmNamespace).
		Get(b.context, b.vmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// No VMI running yet — the oneshot boot hasn't happened, keep waiting.
		return
	}
	if err != nil {
		logrus.WithError(err).Warn("failed to get VMI for oneshot restore")
		return
	}
	if string(vmi.UID) == override.VMIUID {
		// Same VMI generation — oneshot not yet consumed.
		return
	}

	vm, err := b.virtClient.KubevirtV1().VirtualMachines(b.vmNamespace).
		Get(b.context, b.vmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logrus.Info("VM not found, clearing stale oneshot override")
		if err := b.resourceManager.ClearBootOverride(); err != nil {
			logrus.WithError(err).Warn("failed to clear stale boot override")
		}
		return
	}
	if err != nil {
		logrus.WithError(err).Warn("failed to get VM for oneshot restore")
		return
	}
	if vm.Spec.Template == nil {
		return
	}

	logrus.Info("new VMI generation detected after oneshot was set, restoring original boot order")

	patchOps := resourcemanager.BuildBootOrderRestorePatch(vm, override)
	if len(patchOps) > 0 {
		patchData, err := json.Marshal(patchOps)
		if err != nil {
			logrus.WithError(err).Warn("failed to marshal boot order restore patch")
			return
		}
		if _, err := b.virtClient.KubevirtV1().VirtualMachines(b.vmNamespace).
			Patch(b.context, b.vmName, types.JSONPatchType, patchData, metav1.PatchOptions{}); err != nil {
			logrus.WithError(err).Warn("failed to patch VM to restore boot order")
			return
		}
	}

	// Clear only after the VM patch succeeded; a failure retries on the next tick.
	if err := b.resourceManager.ClearBootOverride(); err != nil {
		logrus.WithError(err).Warn("failed to clear boot override, will retry")
		return
	}
	logrus.Info("oneshot boot order restored successfully")
}
