package virtbmc

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/watch"
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

	for {
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
		case event, ok := <-vmiWatch.ResultChan():
			if !ok {
				return true, nil
			}
			if event.Type == watch.Error {
				return true, fmt.Errorf("VMI watch returned an error event")
			}
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
