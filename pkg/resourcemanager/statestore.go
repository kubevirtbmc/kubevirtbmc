package resourcemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// StateStore abstracts where the virtbmc agent keeps per-BMC state.
// Managed mode backs it with the VirtualMachineBMC CR; standalone mode backs
// it with a local file. The agent's boot reconciliation is identical in both
// modes. BootOverride persistence must survive agent restarts because a
// pending oneshot backup is the only record of the state to restore.
type StateStore interface {
	// GetBootOverride returns the active boot override, nil when none.
	GetBootOverride(ctx context.Context) (*bmcv1.BootOverrideStatus, error)
	// SaveBootOverride replaces the whole boot override value. A merge would
	// linger stale keys from a previous override (e.g. bootOrders surviving a
	// oneshot→persistent overwrite).
	SaveBootOverride(ctx context.Context, override *bmcv1.BootOverrideStatus) error
	// ClearBootOverride removes the boot override. Clearing an absent override
	// is not an error.
	ClearBootOverride(ctx context.Context) error
}

// clusterStateStore keeps agent state in status.bootOverride of the
// VirtualMachineBMC CR.
type clusterStateStore struct {
	bmcClient client.Client
	namespace string
	bmcName   string
}

func NewClusterStateStore(bmcClient client.Client, namespace, bmcName string) StateStore {
	return &clusterStateStore{bmcClient: bmcClient, namespace: namespace, bmcName: bmcName}
}

func (s *clusterStateStore) GetBootOverride(ctx context.Context) (*bmcv1.BootOverrideStatus, error) {
	bmc := &bmcv1.VirtualMachineBMC{}
	if err := s.bmcClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
		return nil, err
	}
	return bmc.Status.BootOverride, nil
}

func (s *clusterStateStore) SaveBootOverride(ctx context.Context, override *bmcv1.BootOverrideStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := s.bmcClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
			return fmt.Errorf("failed to get VirtualMachineBMC: %w", err)
		}
		bmc.Status.BootOverride = override
		if err := s.bmcClient.Status().Update(ctx, bmc); err != nil {
			return fmt.Errorf("failed to update VirtualMachineBMC status: %w", err)
		}
		return nil
	})
}

func (s *clusterStateStore) ClearBootOverride(ctx context.Context) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		bmc := &bmcv1.VirtualMachineBMC{}
		if err := s.bmcClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get VirtualMachineBMC: %w", err)
		}
		if bmc.Status.BootOverride == nil {
			return nil
		}
		bmc.Status.BootOverride = nil
		if err := s.bmcClient.Status().Update(ctx, bmc); err != nil {
			return fmt.Errorf("failed to update VirtualMachineBMC status: %w", err)
		}
		return nil
	})
}

// stateFile is the on-disk envelope of the standalone state file. Each
// feature owns a top-level key so later additions (e.g. Redfish task
// records) slot in without reshaping existing files; absent keys decode as
// zero values, so older agents read newer files' known keys and ignore the
// rest.
type stateFile struct {
	BootOverride *bmcv1.BootOverrideStatus `json:"bootOverride,omitempty"`
}

// fileStateStore keeps state in a local JSON file for standalone mode, where
// there is no VirtualMachineBMC CR. Only the boot override is persisted; the
// storage class is startup config held by the resource manager, not state.
type fileStateStore struct {
	path string

	mu       sync.Mutex
	override *bmcv1.BootOverrideStatus
}

// NewFileStateStore loads any previously persisted boot override from path.
func NewFileStateStore(path string) (StateStore, error) {
	s := &fileStateStore{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("failed to read state file %s: %w", path, err)
	}
	var file stateFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("failed to parse state file %s: %w", path, err)
	}
	s.override = file.BootOverride
	return s, nil
}

func (s *fileStateStore) GetBootOverride(context.Context) (*bmcv1.BootOverrideStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.override.DeepCopy(), nil
}

func (s *fileStateStore) SaveBootOverride(_ context.Context, override *bmcv1.BootOverrideStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := override.DeepCopy()
	if err := s.persistLocked(candidate); err != nil {
		return err
	}
	s.override = candidate
	return nil
}

func (s *fileStateStore) ClearBootOverride(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.override == nil {
		return nil
	}
	if err := s.persistLocked(nil); err != nil {
		return err
	}
	s.override = nil
	return nil
}

// persistLocked writes via tmp+rename so a crash mid-write cannot leave a
// truncated state file behind. The in-memory field is only committed by the
// caller after a successful persist, so a failed write cannot leave memory and
// disk disagreeing.
func (s *fileStateStore) persistLocked(override *bmcv1.BootOverrideStatus) error {
	data, err := json.Marshal(stateFile{BootOverride: override})
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
