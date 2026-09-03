package resourcemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// StateStore abstracts where the virtbmc agent keeps per-BMC state and config.
// The cluster deployment backs it with the VirtualMachineBMC CR (shared with
// the bootorderrestore controller); standalone mode backs it with a local
// file. BootOverride persistence must survive agent restarts: a pending
// oneshot backup is the only record of the boot state to restore.
type StateStore interface {
	// GetBootOverride returns the active boot override, nil when none.
	GetBootOverride() (*bmcv1.BootOverrideStatus, error)
	// SaveBootOverride replaces the whole boot override value. A merge would
	// linger stale keys from a previous override (e.g. bootOrders surviving a
	// oneshot→persistent overwrite).
	SaveBootOverride(override *bmcv1.BootOverrideStatus) error
	// ClearBootOverride removes the boot override. Clearing an absent override
	// is not an error.
	ClearBootOverride() error
	// GetStorageClassName returns the StorageClass for virtual media
	// DataVolumes, "" for the cluster default.
	GetStorageClassName() (string, error)
}

// clusterStateStore keeps state in status.bootOverride of the VirtualMachineBMC
// CR, where the bootorderrestore controller can consume oneshot backups.
type clusterStateStore struct {
	ctx       context.Context
	bmcClient client.Client
	namespace string
	bmcName   string
}

func NewClusterStateStore(ctx context.Context, bmcClient client.Client, namespace, bmcName string) StateStore {
	return &clusterStateStore{ctx: ctx, bmcClient: bmcClient, namespace: namespace, bmcName: bmcName}
}

func (s *clusterStateStore) GetBootOverride() (*bmcv1.BootOverrideStatus, error) {
	bmc := &bmcv1.VirtualMachineBMC{}
	if err := s.bmcClient.Get(s.ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
		return nil, err
	}
	return bmc.Status.BootOverride, nil
}

func (s *clusterStateStore) SaveBootOverride(override *bmcv1.BootOverrideStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := s.bmcClient.Get(s.ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
			return fmt.Errorf("failed to get VirtualMachineBMC: %w", err)
		}
		bmc.Status.BootOverride = override
		if err := s.bmcClient.Status().Update(s.ctx, bmc); err != nil {
			return fmt.Errorf("failed to update VirtualMachineBMC status: %w", err)
		}
		return nil
	})
}

func (s *clusterStateStore) ClearBootOverride() error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := s.bmcClient.Get(s.ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get VirtualMachineBMC: %w", err)
		}
		if bmc.Status.BootOverride == nil {
			return nil
		}
		bmc.Status.BootOverride = nil
		if err := s.bmcClient.Status().Update(s.ctx, bmc); err != nil {
			return fmt.Errorf("failed to update VirtualMachineBMC status: %w", err)
		}
		return nil
	})
}

func (s *clusterStateStore) GetStorageClassName() (string, error) {
	var bmc bmcv1.VirtualMachineBMC
	if err := s.bmcClient.Get(s.ctx, types.NamespacedName{Namespace: s.namespace, Name: s.bmcName}, &bmc); err != nil {
		// A missing CR means no StorageClassName override is configured, not a failure.
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if bmc.Spec.StorageClassName != nil {
		return *bmc.Spec.StorageClassName, nil
	}
	return "", nil
}

// fileStateStore keeps state in a local JSON file for standalone mode, where
// there is no VirtualMachineBMC CR. The storage class is static config passed
// at startup, so only the boot override is persisted.
type fileStateStore struct {
	path         string
	storageClass string

	mu       sync.Mutex
	override *bmcv1.BootOverrideStatus
}

// NewFileStateStore loads any previously persisted boot override from path.
func NewFileStateStore(path, storageClass string) (StateStore, error) {
	s := &fileStateStore{path: path, storageClass: storageClass}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("failed to read state file %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.override); err != nil {
		return nil, fmt.Errorf("failed to parse state file %s: %w", path, err)
	}
	return s, nil
}

func (s *fileStateStore) GetBootOverride() (*bmcv1.BootOverrideStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.override.DeepCopy(), nil
}

func (s *fileStateStore) SaveBootOverride(override *bmcv1.BootOverrideStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override = override.DeepCopy()
	return s.persistLocked()
}

func (s *fileStateStore) ClearBootOverride() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.override == nil {
		return nil
	}
	s.override = nil
	return s.persistLocked()
}

// persistLocked writes via tmp+rename so a crash mid-write cannot leave a
// truncated state file behind.
func (s *fileStateStore) persistLocked() error {
	data, err := json.Marshal(s.override)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *fileStateStore) GetStorageClassName() (string, error) {
	return s.storageClass, nil
}
