package resourcemanager

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdifake "kubevirt.io/client-go/containerizeddataimporter/fake"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"

	"kubevirt.io/kubevirtbmc/pkg/builder"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

var errInjected = errors.New("injected")

func cdVolume(source kubevirtv1.VolumeSource) kubevirtv1.Volume {
	return kubevirtv1.Volume{Name: "cdrom", VolumeSource: source}
}

func mediaDV() *cdiv1.DataVolume {
	return builder.NewDataVolumeBuilder(testNamespace, testVMName).
		WithHTTPSource(testImageURL).
		WithStorage(testImageSizeBytes).
		WithAnnotation(util.AnnStorageBindImmediateRequested, "").
		WithLabel(util.LabelVirtualMedia, testVMName).Build()
}

func failing() k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errInjected }
}

func TestIsOrphanVirtualMedia(t *testing.T) {
	vmUID := k8stypes.UID("vm-uid")
	emptyVM := func() *kubevirtv1.VirtualMachine {
		vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).Build()
		vm.UID = vmUID
		return vm
	}
	testCases := []struct {
		name   string
		vm     func() *kubevirtv1.VirtualMachine
		dv     func() *cdiv1.DataVolume
		orphan bool
	}{
		{"unused virtual media", emptyVM, mediaDV, true},
		{"not made by kubevirtbmc (no virtual-media label)", emptyVM, func() *cdiv1.DataVolume {
			dv := mediaDV()
			dv.Labels = nil
			return dv
		}, false},
		{"kubevirtbmc media of another VM", emptyVM, func() *cdiv1.DataVolume {
			dv := mediaDV()
			dv.Labels[util.LabelVirtualMedia] = "other-vm"
			return dv
		}, false},
		{"used as an ephemeral volume", func() *kubevirtv1.VirtualMachine {
			vm := emptyVM()
			vm.Spec.Template.Spec.Volumes = []kubevirtv1.Volume{{Name: "rootdisk",
				VolumeSource: kubevirtv1.VolumeSource{Ephemeral: &kubevirtv1.EphemeralVolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: testVMName}}}}}
			return vm
		}, mediaDV, false},
		{"no HTTP source", emptyVM, func() *cdiv1.DataVolume {
			dv := mediaDV()
			dv.Spec.Source = nil
			return dv
		}, false},
		{"no bind-immediate annotation", emptyVM, func() *cdiv1.DataVolume {
			dv := mediaDV()
			dv.Annotations = nil
			return dv
		}, false},
		{"owned by the VM", emptyVM, func() *cdiv1.DataVolume {
			dv := mediaDV()
			dv.OwnerReferences = []metav1.OwnerReference{{UID: vmUID}}
			return dv
		}, false},
		{"templated by the VM", func() *kubevirtv1.VirtualMachine {
			vm := emptyVM()
			vm.Spec.DataVolumeTemplates = []kubevirtv1.DataVolumeTemplateSpec{{ObjectMeta: metav1.ObjectMeta{Name: testVMName}}}
			return vm
		}, mediaDV, false},
		{"used as a DataVolume volume", func() *kubevirtv1.VirtualMachine {
			vm := emptyVM()
			vm.Spec.Template.Spec.Volumes = []kubevirtv1.Volume{{Name: "rootdisk",
				VolumeSource: kubevirtv1.VolumeSource{DataVolume: &kubevirtv1.DataVolumeSource{Name: testVMName}}}}
			return vm
		}, mediaDV, false},
		{"used as a PVC volume", func() *kubevirtv1.VirtualMachine {
			vm := emptyVM()
			vm.Spec.Template.Spec.Volumes = []kubevirtv1.Volume{{Name: "rootdisk",
				VolumeSource: kubevirtv1.VolumeSource{PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{}}}}
			vm.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = testVMName
			return vm
		}, mediaDV, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.orphan, isOrphanVirtualMedia(tc.vm(), tc.dv()))
		})
	}
}

func TestCdromDataVolume(t *testing.T) {
	_, _, err := cdromDataVolume(builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build())
	require.Error(t, err, "no template")

	_, _, err = cdromDataVolume(builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().Build())
	require.Error(t, err, "no CD-ROM disk")

	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).
		WithVolumes(cdVolume(kubevirtv1.VolumeSource{ContainerDisk: &kubevirtv1.ContainerDiskSource{Image: "iso"}})).Build()
	dv, disk, err := cdromDataVolume(vm)
	require.NoError(t, err)
	require.Empty(t, dv, "a CD-ROM backed by something else is not virtual media")
	require.Equal(t, "cdrom", disk)
}

func TestVirtualMediaErrorPaths(t *testing.T) {
	ctx := context.Background()
	withCD := func() *kubevirtv1.VirtualMachine {
		return builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).
			WithVolumes(cdVolume(kubevirtv1.VolumeSource{DataVolume: &kubevirtv1.DataVolumeSource{Name: testVMName, Hotpluggable: true}})).Build()
	}
	empty := func() *kubevirtv1.VirtualMachine {
		return builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).Build()
	}
	manager := func(vm *kubevirtv1.VirtualMachine, dvs ...runtime.Object) (*VirtualMachineResourceManager, *kubevirtfake.Clientset, *cdifake.Clientset) {
		virt := kubevirtfake.NewSimpleClientset(vm)
		cdi := cdifake.NewSimpleClientset(dvs...)
		return &VirtualMachineResourceManager{virtClient: virt, cdiClient: cdi, namespace: testNamespace, name: testVMName,
			virtualMedia: &fakeVirtualMedia{}}, virt, cdi
	}

	t.Run("uninitialized virtual media", func(t *testing.T) {
		m, _, _ := manager(empty())
		m.virtualMedia = nil
		_, err := m.GetVirtualMedia(ctx)
		require.Error(t, err)
	})
	t.Run("GetVirtualMedia: VM read fails", func(t *testing.T) {
		m, virt, _ := manager(empty())
		virt.PrependReactor("get", "virtualmachines", failing())
		_, err := m.GetVirtualMedia(ctx)
		require.ErrorIs(t, err, errInjected)
	})
	t.Run("GetVirtualMedia: VM without template or CD-ROM is empty", func(t *testing.T) {
		for _, vm := range []*kubevirtv1.VirtualMachine{
			builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().Build(),
		} {
			m, _, _ := manager(vm)
			_, err := m.GetVirtualMedia(ctx)
			require.NoError(t, err)
			require.Equal(t, &fakeVirtualMedia{called: true}, m.virtualMedia)
		}
	})
	t.Run("GetVirtualMedia: DataVolume read fails", func(t *testing.T) {
		m, _, cdi := manager(withCD())
		cdi.PrependReactor("get", "datavolumes", failing())
		_, err := m.GetVirtualMedia(ctx)
		require.ErrorIs(t, err, errInjected)
	})
	t.Run("EjectMedia: VM update fails", func(t *testing.T) {
		m, virt, _ := manager(withCD(), mediaDV())
		virt.PrependReactor("update", "virtualmachines", failing())
		require.ErrorIs(t, m.EjectMedia(ctx), errInjected)
	})
	t.Run("EjectMedia: DataVolume delete fails after detaching", func(t *testing.T) {
		m, _, cdi := manager(withCD(), mediaDV())
		cdi.PrependReactor("delete", "datavolumes", failing())
		require.ErrorIs(t, m.EjectMedia(ctx), errInjected)
		require.Equal(t, &fakeVirtualMedia{called: true}, m.virtualMedia, "the VM no longer has the media")
	})
	t.Run("EjectMedia: DataVolume read fails after detaching", func(t *testing.T) {
		m, _, cdi := manager(withCD(), mediaDV())
		cdi.PrependReactor("get", "datavolumes", failing())
		require.ErrorIs(t, m.EjectMedia(ctx), errInjected)
	})
	t.Run("EjectMedia: VM without template", func(t *testing.T) {
		m, _, _ := manager(builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build())
		require.Error(t, m.EjectMedia(ctx))
	})
	t.Run("InsertMedia: waiting for the leftover fails", func(t *testing.T) {
		m, _, cdi := manager(empty(), mediaDV())
		gets := 0
		cdi.PrependReactor("get", "datavolumes", func(k8stesting.Action) (bool, runtime.Object, error) {
			gets++
			if gets == 1 {
				return false, nil, nil // the first lookup finds the leftover
			}
			return true, nil, errInjected
		})
		require.ErrorIs(t, m.InsertMedia(ctx, testImageURL), errInjected)
	})
	t.Run("EjectMedia: leftover lookup fails", func(t *testing.T) {
		m, _, cdi := manager(empty())
		cdi.PrependReactor("get", "datavolumes", failing())
		require.ErrorIs(t, m.EjectMedia(ctx), errInjected)
	})
	t.Run("EjectMedia: leftover delete fails", func(t *testing.T) {
		m, _, cdi := manager(empty(), mediaDV())
		cdi.PrependReactor("delete", "datavolumes", failing())
		require.ErrorIs(t, m.EjectMedia(ctx), errInjected)
	})
	t.Run("InsertMedia: name taken by a non-media DataVolume", func(t *testing.T) {
		userDisk := builder.NewDataVolumeBuilder(testNamespace, testVMName).WithHTTPSource(testImageURL).WithStorage(testImageSizeBytes).Build()
		m, _, cdi := manager(empty(), userDisk)
		require.Error(t, m.InsertMedia(ctx, testImageURL))
		got, err := cdi.CdiV1beta1().DataVolumes(testNamespace).Get(ctx, testVMName, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, userDisk, got, "the user's DataVolume is untouched")
	})
}

// Media a user attached to the CD-ROM is detached by an eject, never deleted:
// only DataVolumes kubevirtbmc created (LabelVirtualMedia) are (#251).
func TestEjectLeavesUserMedia(t *testing.T) {
	ctx := context.Background()
	newManager := func(vm *kubevirtv1.VirtualMachine, dvs ...runtime.Object) (*VirtualMachineResourceManager, *kubevirtfake.Clientset, *cdifake.Clientset) {
		virt := kubevirtfake.NewSimpleClientset(vm)
		cdi := cdifake.NewSimpleClientset(dvs...)
		return &VirtualMachineResourceManager{virtClient: virt, cdiClient: cdi, namespace: testNamespace, name: testVMName,
			virtualMedia: &fakeVirtualMedia{}}, virt, cdi
	}
	requireDV := func(t *testing.T, cdi *cdifake.Clientset, name string) {
		t.Helper()
		_, err := cdi.CdiV1beta1().DataVolumes(testNamespace).Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err, "DataVolume %s must be kept", name)
	}

	t.Run("a user's DataVolume on the CD-ROM is detached and kept", func(t *testing.T) {
		userISO := builder.NewDataVolumeBuilder(testNamespace, "user-iso").WithHTTPSource(testImageURL).WithStorage(testImageSizeBytes).Build()
		vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).
			WithVolumes(cdVolume(kubevirtv1.VolumeSource{DataVolume: &kubevirtv1.DataVolumeSource{Name: "user-iso"}})).Build()
		m, virt, cdi := newManager(vm, userISO)

		vmedia, err := m.GetVirtualMedia(ctx)
		require.NoError(t, err)
		require.Equal(t, &fakeVirtualMedia{called: true, imageURL: testImageURL, inserted: true}, vmedia, "the drive holds media")

		require.NoError(t, m.EjectMedia(ctx))
		requireDV(t, cdi, "user-iso")
		got, err := virt.KubevirtV1().VirtualMachines(testNamespace).Get(ctx, testVMName, metav1.GetOptions{})
		require.NoError(t, err)
		require.Empty(t, got.Spec.Template.Spec.Volumes, "the CD-ROM is empty")
	})

	t.Run("a DataVolume named after the VM in use as an ephemeral volume is kept", func(t *testing.T) {
		// Looks like media (HTTP source, bind-immediate) but kubevirtbmc did
		// not make it.
		disk := builder.NewDataVolumeBuilder(testNamespace, testVMName).WithHTTPSource(testImageURL).WithStorage(testImageSizeBytes).
			WithAnnotation(util.AnnStorageBindImmediateRequested, "").Build()
		vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).
			WithVolumes(kubevirtv1.Volume{Name: "rootdisk", VolumeSource: kubevirtv1.VolumeSource{Ephemeral: &kubevirtv1.EphemeralVolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: testVMName}}}}).Build()
		m, _, cdi := newManager(vm, disk)
		require.NoError(t, m.EjectMedia(ctx))
		requireDV(t, cdi, testVMName)
		require.Error(t, m.InsertMedia(ctx, testImageURL), "the name is taken by the user's disk")
		requireDV(t, cdi, testVMName)
	})

	t.Run("media kubevirtbmc inserted is deleted on eject", func(t *testing.T) {
		vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().WithCDRomDisk("cdrom", nil).
			WithVolumes(cdVolume(kubevirtv1.VolumeSource{DataVolume: &kubevirtv1.DataVolumeSource{Name: testVMName, Hotpluggable: true}})).Build()
		m, _, cdi := newManager(vm, mediaDV())
		require.NoError(t, m.EjectMedia(ctx))
		_, err := cdi.CdiV1beta1().DataVolumes(testNamespace).Get(ctx, testVMName, metav1.GetOptions{})
		require.Error(t, err, "deleted")
	})
}
