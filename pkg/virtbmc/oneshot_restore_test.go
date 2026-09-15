package virtbmc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8swatch "k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/builder"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

type countingStateStore struct {
	gets atomic.Int32
}

func (s *countingStateStore) GetBootOverride(context.Context) (*bmcv1.BootOverrideStatus, error) {
	s.gets.Add(1)
	return nil, nil
}

func (*countingStateStore) SaveBootOverride(context.Context, *bmcv1.BootOverrideStatus) error {
	return nil
}

func (*countingStateStore) ClearBootOverride(context.Context) error {
	return nil
}

func TestBootOverrideReconcileSleepsWithoutActiveOverride(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &countingStateStore{}
	vm := builder.NewVirtualMachineBuilder("default", "testvm").WithDisk("root", nil).Build()
	virtClient := kubevirtfake.NewSimpleClientset(vm)
	manager := resourcemanager.NewVirtualMachineResourceManager(
		virtClient, nil, store, nil, "", nil, 0, false, "",
	)
	if err := manager.Initialize(ctx, vm.Namespace, vm.Name); err != nil {
		t.Fatalf("failed to initialize resource manager: %v", err)
	}
	bmc := &VirtBMC{context: ctx, resourceManager: manager}

	done := make(chan struct{})
	go func() {
		defer close(done)
		bmc.runBootOverrideReconcile(false)
	}()

	time.Sleep(20 * time.Millisecond)
	if got := store.gets.Load(); got != 0 {
		t.Fatalf("idle coordinator read StateStore %d times", got)
	}

	if err := manager.SetBootDevice(ctx, resourcemanager.BootDeviceHdd, nil); err != nil {
		t.Fatalf("failed to notify coordinator: %v", err)
	}
	deadline := time.After(time.Second)
	for store.gets.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("coordinator did not reconcile after state change")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	readsAfterChange := store.gets.Load()
	time.Sleep(20 * time.Millisecond)
	if got := store.gets.Load(); got != readsAfterChange {
		t.Fatalf("idle coordinator kept reading StateStore: before=%d after=%d", readsAfterChange, got)
	}
	for _, action := range virtClient.Actions() {
		if action.GetVerb() == "watch" {
			t.Fatal("coordinator started a watch without an active override")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after context cancellation")
	}
}

func TestBootOverrideReconcileUsesVMIWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vm := builder.NewVirtualMachineBuilder("default", "testvm").
		WithDisk("root", ptr.To[uint](2)).
		Build()
	vm.UID = types.UID("vm-uid")
	vmi := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: vm.Namespace,
			Name:      vm.Name,
			UID:       types.UID("vmi-old"),
		},
	}
	virtClient := kubevirtfake.NewSimpleClientset(vm, vmi)
	store, err := resourcemanager.NewFileStateStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("failed to create state store: %v", err)
	}
	if err := store.SaveBootOverride(ctx, &bmcv1.BootOverrideStatus{
		Mode:       bmcv1.BootOverrideModeOneshot,
		VMUID:      string(vm.UID),
		VMIUID:     string(vmi.UID),
		BootOrders: map[string]uint{"disk:root": 1},
	}); err != nil {
		t.Fatalf("failed to save boot override: %v", err)
	}

	manager := resourcemanager.NewVirtualMachineResourceManager(virtClient, nil, store, nil, "", nil, 0, false, "")
	if err := manager.Initialize(ctx, vm.Namespace, vm.Name); err != nil {
		t.Fatalf("failed to initialize resource manager: %v", err)
	}
	bmc := &VirtBMC{context: ctx, resourceManager: manager}
	done := make(chan struct{})
	go func() {
		defer close(done)
		bmc.runBootOverrideReconcile(true)
	}()

	waitFor(t, func() bool { return watchCount(virtClient) == 2 })
	watched := map[string]bool{}
	for _, action := range virtClient.Actions() {
		if action.GetVerb() != "watch" {
			continue
		}
		watched[action.GetResource().Resource] = true
		watchAction := action.(k8stesting.WatchAction)
		if got := watchAction.GetWatchRestrictions().Fields.String(); got != "metadata.name=testvm" {
			t.Fatalf("watch was not scoped to the target VM: %q", got)
		}
	}
	for _, resource := range []string{"virtualmachines", "virtualmachineinstances"} {
		if !watched[resource] {
			t.Fatalf("coordinator did not watch %s", resource)
		}
	}

	if err := virtClient.KubevirtV1().VirtualMachineInstances(vm.Namespace).
		Delete(ctx, vm.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("failed to delete old VMI: %v", err)
	}
	vmi.UID = types.UID("vmi-new")
	if _, err := virtClient.KubevirtV1().VirtualMachineInstances(vm.Namespace).
		Create(ctx, vmi, metav1.CreateOptions{}); err != nil {
		t.Fatalf("failed to create new VMI: %v", err)
	}

	waitFor(t, func() bool {
		override, err := store.GetBootOverride(ctx)
		return err == nil && override == nil
	})
	restored, err := virtClient.KubevirtV1().VirtualMachines(vm.Namespace).
		Get(ctx, vm.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get restored VM: %v", err)
	}
	if got := restored.Spec.Template.Spec.Domain.Devices.Disks[0].BootOrder; got == nil || *got != 1 {
		t.Fatalf("boot order was not restored, got %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after context cancellation")
	}
}

func TestBootOverrideReconcileWatchesPersistentOverrideUntilVMDeletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vm := builder.NewVirtualMachineBuilder("default", "testvm").
		WithDisk("root", ptr.To[uint](1)).
		Build()
	vm.UID = types.UID("vm-uid")
	virtClient := kubevirtfake.NewSimpleClientset(vm)
	store, err := resourcemanager.NewFileStateStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("failed to create state store: %v", err)
	}
	if err := store.SaveBootOverride(ctx, &bmcv1.BootOverrideStatus{
		Mode:  bmcv1.BootOverrideModePersistent,
		VMUID: string(vm.UID),
	}); err != nil {
		t.Fatalf("failed to save boot override: %v", err)
	}

	manager := resourcemanager.NewVirtualMachineResourceManager(virtClient, nil, store, nil, "", nil, 0, false, "")
	if err := manager.Initialize(ctx, vm.Namespace, vm.Name); err != nil {
		t.Fatalf("failed to initialize resource manager: %v", err)
	}
	bmc := &VirtBMC{context: ctx, resourceManager: manager}
	done := make(chan struct{})
	go func() {
		defer close(done)
		bmc.runBootOverrideReconcile(true)
	}()

	waitFor(t, func() bool { return watchCount(virtClient) == 2 })
	if err := virtClient.KubevirtV1().VirtualMachines(vm.Namespace).
		Delete(ctx, vm.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("failed to delete VM: %v", err)
	}
	waitFor(t, func() bool {
		override, err := store.GetBootOverride(ctx)
		return err == nil && override == nil
	})

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after context cancellation")
	}
}

func TestBootOverrideReconcileReconnectsClosedWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vm := builder.NewVirtualMachineBuilder("default", "testvm").
		WithDisk("root", ptr.To[uint](2)).
		Build()
	vm.UID = types.UID("vm-uid")
	vmi := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: vm.Namespace,
			Name:      vm.Name,
			UID:       types.UID("vmi-current"),
		},
	}
	virtClient := kubevirtfake.NewSimpleClientset(vm, vmi)
	var (
		watchMu     sync.Mutex
		vmiWatchers []*k8swatch.RaceFreeFakeWatcher
	)
	virtClient.PrependWatchReactor("*", func(action k8stesting.Action) (bool, k8swatch.Interface, error) {
		watcher := k8swatch.NewRaceFreeFake()
		if action.GetResource().Resource == "virtualmachineinstances" {
			watchMu.Lock()
			vmiWatchers = append(vmiWatchers, watcher)
			watchMu.Unlock()
		}
		return true, watcher, nil
	})

	store, err := resourcemanager.NewFileStateStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("failed to create state store: %v", err)
	}
	if err := store.SaveBootOverride(ctx, &bmcv1.BootOverrideStatus{
		Mode:       bmcv1.BootOverrideModeOneshot,
		VMUID:      string(vm.UID),
		VMIUID:     string(vmi.UID),
		BootOrders: map[string]uint{"disk:root": 1},
	}); err != nil {
		t.Fatalf("failed to save boot override: %v", err)
	}
	manager := resourcemanager.NewVirtualMachineResourceManager(virtClient, nil, store, nil, "", nil, 0, false, "")
	if err := manager.Initialize(ctx, vm.Namespace, vm.Name); err != nil {
		t.Fatalf("failed to initialize resource manager: %v", err)
	}
	bmc := &VirtBMC{context: ctx, resourceManager: manager}
	done := make(chan struct{})
	go func() {
		defer close(done)
		bmc.runBootOverrideReconcile(true)
	}()

	waitFor(t, func() bool {
		watchMu.Lock()
		defer watchMu.Unlock()
		return len(vmiWatchers) == 1
	})
	watchMu.Lock()
	first := vmiWatchers[0]
	watchMu.Unlock()
	first.Stop()
	waitFor(t, func() bool {
		watchMu.Lock()
		defer watchMu.Unlock()
		return len(vmiWatchers) == 2
	})

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after context cancellation")
	}
}

func watchCount(client *kubevirtfake.Clientset) int {
	watches := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "watch" {
			watches++
		}
	}
	return watches
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(time.Millisecond)
	}
}
