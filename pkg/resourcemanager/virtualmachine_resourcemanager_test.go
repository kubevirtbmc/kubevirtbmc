package resourcemanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdifake "kubevirt.io/client-go/containerizeddataimporter/fake"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"

	"kubevirt.io/kubevirtbmc/pkg/builder"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

const (
	testNamespace      = "test-namespace"
	testVMName         = "test-vm"
	testImageURL       = "http://127.0.0.1/test.iso"
	testImageSizeBytes = int64(1 << 30) // 1 GiB = 1073741824
)

type fakeVirtualMedia struct {
	called   bool
	imageURL string
	inserted bool
}

func (f *fakeVirtualMedia) Id() string {
	panic("implement me")
}

func (f *fakeVirtualMedia) OdataId() string {
	panic("implement me")
}

func (f *fakeVirtualMedia) Manage(OdataInterface) error {
	panic("implement me")
}

func (f *fakeVirtualMedia) ManagedBy(OdataInterface) error {
	panic("implement me")
}

func (f *fakeVirtualMedia) SetVirtualMedia(imageURL string, inserted bool) {
	f.called = true
	f.imageURL = imageURL
	f.inserted = inserted
}

func TestVirtualMachineResourceManager_EjectMedia(t *testing.T) {
	testCases := []struct {
		name                 string
		virtualMedia         VirtualMediaInterface
		dv                   *cdiv1.DataVolume
		vm                   *kubevirtv1.VirtualMachine
		expectedVirtualMedia VirtualMediaInterface
		expectedDV           *cdiv1.DataVolume
		expectedVM           *kubevirtv1.VirtualMachine
		shouldError          bool
	}{
		{
			name:         "Eject media from a virtual machine who already has media inserted should success",
			virtualMedia: &fakeVirtualMedia{},
			dv: builder.NewDataVolumeBuilder(testNamespace, testVMName).
				WithHTTPSource(testImageURL).
				WithStorage(testImageSizeBytes).Build(),
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes(kubevirtv1.Volume{
					Name: "cdrom",
					VolumeSource: kubevirtv1.VolumeSource{
						DataVolume: &kubevirtv1.DataVolumeSource{
							Name:         testVMName,
							Hotpluggable: true,
						},
					},
				}).Build(),
			expectedVirtualMedia: &fakeVirtualMedia{
				called:   true,
				imageURL: "",
				inserted: false,
			},
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes().Build(),
			shouldError: false,
		},
		{
			name:         "Eject media from a virtual machine who already has media inserted and other volumes should success",
			virtualMedia: &fakeVirtualMedia{},
			dv: builder.NewDataVolumeBuilder(testNamespace, testVMName).
				WithHTTPSource(testImageURL).
				WithStorage(testImageSizeBytes).Build(),
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes([]kubevirtv1.Volume{
					{
						Name: "default",
						VolumeSource: kubevirtv1.VolumeSource{
							PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{},
						},
					},
					{
						Name: "cdrom",
						VolumeSource: kubevirtv1.VolumeSource{
							DataVolume: &kubevirtv1.DataVolumeSource{
								Name:         testVMName,
								Hotpluggable: true,
							},
						},
					},
				}...).Build(),
			expectedVirtualMedia: &fakeVirtualMedia{
				called:   true,
				imageURL: "",
				inserted: false,
			},
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes([]kubevirtv1.Volume{
					{
						Name: "default",
						VolumeSource: kubevirtv1.VolumeSource{
							PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{},
						},
					},
				}...).Build(),
			shouldError: false,
		},
		{
			name:         "Eject media from a virtual machine who doesn't have media inserted should fail",
			virtualMedia: &fakeVirtualMedia{},
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes([]kubevirtv1.Volume{
					{
						Name: "default",
						VolumeSource: kubevirtv1.VolumeSource{
							PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{},
						},
					},
				}...).Build(),
			expectedVirtualMedia: &fakeVirtualMedia{
				called:   false,
				imageURL: "",
				inserted: false,
			},
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes([]kubevirtv1.Volume{
					{
						Name: "default",
						VolumeSource: kubevirtv1.VolumeSource{
							PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{},
						},
					},
				}...).Build(),
			shouldError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)
			fakeCdiClient := cdifake.NewSimpleClientset()
			if tc.dv != nil {
				err := fakeCdiClient.Tracker().Add(tc.dv)
				require.NoError(t, err, "Mock resource should add into fake client tracker")
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:          context.TODO(),
				virtClient:   fakeVirtClient,
				cdiClient:    fakeCdiClient,
				namespace:    testNamespace,
				name:         testVMName,
				virtualMedia: tc.virtualMedia,
			}

			err := vmrm.EjectMedia()
			if tc.shouldError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			dv, err := fakeCdiClient.CdiV1beta1().DataVolumes(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			if tc.expectedDV == nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedDV, dv)
			}

			vm, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			if tc.expectedVM == nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedVM, vm)
			}

			if tc.expectedVirtualMedia != nil {
				require.Equal(t, tc.expectedVirtualMedia, vmrm.virtualMedia)
			}
		})
	}
}

func TestVirtualMachineResourceManager_InsertMedia(t *testing.T) {
	// Mock HTTP server for GetRemoteFileSize
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(testImageSizeBytes, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	imageURL := server.URL + "/test.iso"

	testCases := []struct {
		name                 string
		imageURL             string
		virtualMedia         VirtualMediaInterface
		dv                   *cdiv1.DataVolume
		vm                   *kubevirtv1.VirtualMachine
		expectedVirtualMedia VirtualMediaInterface
		expectedDV           *cdiv1.DataVolume
		expectedVM           *kubevirtv1.VirtualMachine
		shouldError          bool
	}{
		{
			name:         "Insert media into a virtual machine who has no media inserted should success",
			imageURL:     imageURL,
			virtualMedia: &fakeVirtualMedia{},
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).Build(),
			expectedVirtualMedia: &fakeVirtualMedia{
				called:   true,
				imageURL: imageURL,
				inserted: true,
			},
			expectedDV: builder.NewDataVolumeBuilder(testNamespace, testVMName).
				WithHTTPSource(imageURL).
				WithStorage(testImageSizeBytes).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes(kubevirtv1.Volume{
					Name: "cdrom",
					VolumeSource: kubevirtv1.VolumeSource{
						DataVolume: &kubevirtv1.DataVolumeSource{
							Name:         testVMName,
							Hotpluggable: true,
						},
					},
				}).Build(),
			shouldError: false,
		},
		{
			name:     "Insert media into a virtual machine who has uninitialized virtual media should fail",
			imageURL: imageURL,
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().Build(),
			shouldError: true,
		},
		{
			name:         "Insert media into a virtual machine who has empty template should fail",
			imageURL:     imageURL,
			virtualMedia: &fakeVirtualMedia{},
			vm:           builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			expectedVM:   builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			shouldError:  true,
		},
		{
			name:         "Insert media into a virtual machine who has no CD-ROM disks should fail",
			imageURL:     imageURL,
			virtualMedia: &fakeVirtualMedia{},
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithDisk("default", nil).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithDisk("default", nil).Build(),
			shouldError: true,
		},
		{
			name:         "Insert media into a virtual machine who has multiple CD-ROM disks should success",
			imageURL:     imageURL,
			virtualMedia: &fakeVirtualMedia{},
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithCDRomDisk("cdrom-2", nil).Build(),
			expectedVirtualMedia: &fakeVirtualMedia{
				called:   true,
				imageURL: imageURL,
				inserted: true,
			},
			expectedDV: builder.NewDataVolumeBuilder(testNamespace, testVMName).
				WithHTTPSource(imageURL).
				WithStorage(testImageSizeBytes).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithCDRomDisk("cdrom-2", nil).
				WithVolumes(kubevirtv1.Volume{
					Name: "cdrom",
					VolumeSource: kubevirtv1.VolumeSource{
						DataVolume: &kubevirtv1.DataVolumeSource{
							Name:         testVMName,
							Hotpluggable: true,
						},
					},
				}).Build(),
			shouldError: false,
		},
		{
			name:         "Insert media into a virtual machine who already has media inserted should fail",
			imageURL:     imageURL,
			virtualMedia: &fakeVirtualMedia{},
			dv: builder.NewDataVolumeBuilder(testNamespace, testVMName).
				WithHTTPSource(imageURL).
				WithStorage(testImageSizeBytes).Build(),
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes(kubevirtv1.Volume{
					Name: "cdrom",
					VolumeSource: kubevirtv1.VolumeSource{
						DataVolume: &kubevirtv1.DataVolumeSource{
							Name:         testVMName,
							Hotpluggable: true,
						},
					},
				}).Build(),
			expectedDV: builder.NewDataVolumeBuilder(testNamespace, testVMName).
				WithHTTPSource(imageURL).
				WithStorage(testImageSizeBytes).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithTemplate().
				WithCDRomDisk("cdrom", nil).
				WithVolumes(kubevirtv1.Volume{
					Name: "cdrom",
					VolumeSource: kubevirtv1.VolumeSource{
						DataVolume: &kubevirtv1.DataVolumeSource{
							Name:         testVMName,
							Hotpluggable: true,
						},
					},
				}).Build(),
			shouldError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)
			fakeCdiClient := cdifake.NewSimpleClientset()
			if tc.dv != nil {
				err := fakeCdiClient.Tracker().Add(tc.dv)
				require.NoError(t, err, "Mock resource should add into fake client tracker")
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:          context.TODO(),
				virtClient:   fakeVirtClient,
				cdiClient:    fakeCdiClient,
				namespace:    testNamespace,
				name:         testVMName,
				virtualMedia: tc.virtualMedia,
			}

			err := vmrm.InsertMedia(tc.imageURL)
			if tc.shouldError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			dv, err := fakeCdiClient.CdiV1beta1().DataVolumes(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			if tc.expectedDV == nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedDV, dv)
			}

			vm, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			if tc.expectedVM == nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedVM, vm)
			}

			if tc.expectedVirtualMedia != nil {
				require.Equal(t, tc.expectedVirtualMedia, vmrm.virtualMedia)
			}
		})
	}
}

func TestVirtualMachineResourceManager_GetPowerStatus(t *testing.T) {
	testCases := []struct {
		name        string
		vm          *kubevirtv1.VirtualMachine
		expect      bool
		shouldError bool
	}{
		{
			name:        "Virtual machine is ready",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			expect:      true,
			shouldError: false,
		},
		{
			name:        "Virtual machine is not ready",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(false).Build(),
			expect:      false,
			shouldError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			status, err := vmrm.GetPowerStatus()
			if tc.shouldError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.expect, status)
		})
	}
}

func TestVirtualMachineResourceManager_PowerOn(t *testing.T) {
	testCases := []struct {
		name        string
		vm          *kubevirtv1.VirtualMachine
		expectedVM  *kubevirtv1.VirtualMachine
		shouldError bool
	}{
		{
			name:        "Power on a virtual machine that should be on should have no effect",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(true).Build(),
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(true).Build(),
			shouldError: false,
		},
		{
			name:        "Power on a virtual machine that should be off should succeed",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(false).Build(),
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(true).Build(),
			shouldError: false,
		},
		{
			name: "Power on a virtual machine whose RunStrategy is set to RerunOnFailure should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).Build(),
			shouldError: false,
		},
		{
			name: "Power on a virtual machine whose RunStrategy is set to Halted should make it become RerunOnFailure",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyHalted).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).Build(),
			shouldError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.PowerOn()
			if tc.shouldError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			vm, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.expectedVM, vm)
		})
	}
}

func TestVirtualMachineResourceManager_PowerOff(t *testing.T) {
	testCases := []struct {
		name        string
		vm          *kubevirtv1.VirtualMachine
		expectedVM  *kubevirtv1.VirtualMachine
		shouldError bool
	}{
		{
			name:        "Power off a virtual machine that should be on should succeed",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(true).Build(),
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(false).Build(),
			shouldError: false,
		},
		{
			name:        "Power off a virtual machine that should be off should have no effect",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(false).Build(),
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(false).Build(),
			shouldError: false,
		},
		{
			name: "Power on a virtual machine whose RunStrategy is set to RerunOnFailure should make it become Halted",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyHalted).Build(),
			shouldError: false,
		},
		{
			name: "Power on a virtual machine whose RunStrategy is set to Halted should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyHalted).Build(),
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyHalted).Build(),
			shouldError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.PowerOff()
			if tc.shouldError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			vm, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.expectedVM, vm)
		})
	}
}

func TestVirtualMachineResourceManager_PowerCycle(t *testing.T) {
	testCases := []struct {
		name       string
		vm         *kubevirtv1.VirtualMachine
		vmi        *kubevirtv1.VirtualMachineInstance
		shouldFail bool
	}{
		{
			name:       "Power cycle a running virtual machine should trigger VM restart",
			vm:         builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			vmi:        builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			shouldFail: false,
		},
		{
			name:       "Power cycle a halted virtual machine should trigger VM start via PowerOn",
			vm:         builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			vmi:        nil,
			shouldFail: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)
			if tc.vmi != nil {
				err := fakeVirtClient.Tracker().Add(tc.vmi)
				require.NoError(t, err, "Mock resource should add into fake client tracker")
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.PowerCycle()
			if tc.shouldFail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			vm, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			require.NoError(t, err)
			require.NotNil(t, vm)

			if tc.vmi != nil {
				require.Equal(t, testVMName, vm.Name)
			} else {
				require.NotNil(t, vm.Spec.Running)
				require.True(t, *vm.Spec.Running)
			}
		})
	}
}

func TestVirtualMachineResourceManager_SetBootDevice(t *testing.T) {
	testCases := []struct {
		name        string
		vm          *kubevirtv1.VirtualMachine
		bootDevice  BootDevice
		expectedVM  *kubevirtv1.VirtualMachine
		shouldError bool
	}{
		{
			name: "Set boot device to HDD for a virtual machine with single disk should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to HDD for a virtual machine with single disk whose boot order is already set to 1 should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to HDD for a virtual machine with single disk whose boot order is already set to 2 should bring it to 1",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](2)).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to HDD for a virtual machine with multiple disks should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk-1", nil).
				WithDisk("test-disk-2", nil).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk-1", util.Ptr[uint](1)).
				WithDisk("test-disk-2", nil).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to HDD for a virtual machine with multiple disks whose boot order is already set to 1 should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk-1", util.Ptr[uint](1)).
				WithDisk("test-disk-2", nil).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk-1", util.Ptr[uint](1)).
				WithDisk("test-disk-2", nil).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to HDD for a virtual machine with multiple disks whose boot order is already set to 1 should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk-1", nil).
				WithDisk("test-disk-2", util.Ptr[uint](1)).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk-1", util.Ptr[uint](1)).
				WithDisk("test-disk-2", nil).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with single interface should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with single interface whose boot order is already set to 1 should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with single interface whose boot order is already set to 2 should bring it to 1",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", util.Ptr[uint](2)).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with multiple interfaces should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface-1", nil).
				WithInterface("test-interface-2", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface-1", util.Ptr[uint](1)).
				WithInterface("test-interface-2", nil).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with multiple interfaces whose boot order is already set to 1 should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface-1", util.Ptr[uint](1)).
				WithInterface("test-interface-2", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface-1", util.Ptr[uint](1)).
				WithInterface("test-interface-2", nil).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with multiple interfaces whose boot order is already set to 1 should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface-1", nil).
				WithInterface("test-interface-2", util.Ptr[uint](1)).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface-1", util.Ptr[uint](1)).
				WithInterface("test-interface-2", nil).Build(),
			shouldError: false,
		},
		{
			name:        "Set boot device to HDD for a virtual machine with no disks and interfaces should fail",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			bootDevice:  BootDeviceHdd,
			shouldError: true,
		},
		{
			name: "Set boot device to HDD for a virtual machine with no disks but interfaces should fail",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("test-interface", nil).Build(),
			bootDevice:  BootDeviceHdd,
			shouldError: true,
		},
		{
			name:        "Set boot device to PXE for a virtual machine with no disks and interfaces should fail",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			bootDevice:  BootDevicePxe,
			shouldError: true,
		},
		{
			name: "Set boot device to PXE for a virtual machine with no interfaces but disks should fail",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).Build(),
			bootDevice:  BootDevicePxe,
			shouldError: true,
		},
		{
			name: "Set boot device to HDD for a virtual machine with disks and interfaces but no boot order specified should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", nil).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).
				WithInterface("test-interface", nil).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to HDD for a virtual machine with disks and interfaces and with first boot order set to disk should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).
				WithInterface("test-interface", nil).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).
				WithInterface("test-interface", nil).Build(),
			shouldError: false,
		},
		// {
		// 	name: "Set boot device to HDD for a virtual machine with disks and interfaces and with first boot order set to disk should have no effect",
		// 	vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](1)).
		// 		AddInterface("test-interface", util.Ptr[uint](2)).Build(),
		// 	bootDevice: BootDeviceHdd,
		// 	expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](1)).
		// 		AddInterface("test-interface", util.Ptr[uint](2)).Build(),
		// 	shouldError: false,
		// },
		{
			name: "Set boot device to HDD for a virtual machine with disks and interfaces and with first boot order set to interface should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).
				WithInterface("test-interface", nil).Build(),
			shouldError: false,
		},
		// {
		// 	name: "Set boot device to HDD for a virtual machine with disks and interfaces and with first boot order set to interface should succeed",
		// 	vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](2)).
		// 		AddInterface("test-interface", util.Ptr[uint](1)).Build(),
		// 	bootDevice: BootDeviceHdd,
		// 	expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](1)).
		// 		AddInterface("test-interface", util.Ptr[uint](2)).Build(),
		// 	shouldError: false,
		// },
		{
			name: "Set boot device to PXE for a virtual machine with disks and interfaces but no boot order specified should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "Set boot device to PXE for a virtual machine with disks and interfaces and with first boot order set to disk should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", util.Ptr[uint](1)).
				WithInterface("test-interface", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		// {
		// 	name: "Set boot device to PXE for a virtual machine with disks and interfaces and with first boot order set to disk should succeed",
		// 	vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](1)).
		// 		AddInterface("test-interface", util.Ptr[uint](2)).Build(),
		// 	bootDevice: BootDevicePxe,
		// 	expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](2)).
		// 		AddInterface("test-interface", util.Ptr[uint](1)).Build(),
		// 	shouldError: false,
		// },
		{
			name: "Set boot device to PXE for a virtual machine with disks and interfaces and with first boot order set to interface should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("test-disk", nil).
				WithInterface("test-interface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		// {
		// 	name: "Set boot device to PXE for a virtual machine with disks and interfaces and with first boot order set to interface should have no effect",
		// 	vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](2)).
		// 		AddInterface("test-interface", util.Ptr[uint](1)).Build(),
		// 	bootDevice: BootDevicePxe,
		// 	expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		// 		AddDisk("test-disk", util.Ptr[uint](2)).
		// 		AddInterface("test-interface", util.Ptr[uint](1)).Build(),
		// 	shouldError: false,
		// },
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.SetBootDevice(tc.bootDevice)
			if tc.shouldError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			vm, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).Get(context.TODO(), testVMName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.expectedVM, vm)
		})
	}
}
