package resourcemanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdifake "kubevirt.io/client-go/containerizeddataimporter/fake"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
	kubevirttesting "kubevirt.io/client-go/testing"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/builder"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

const (
	testNamespace      = "test-namespace"
	testVMName         = "test-vm"
	testBMCName        = "test-bmc"
	testImageURL       = "http://127.0.0.1/test.iso"
	testImageSizeBytes = int64(1 << 30) // 1 GiB = 1073741824
)

type fakeVirtualMedia struct {
	called   bool
	imageURL string
	inserted bool
}

func newTestBMC() *bmcv1.VirtualMachineBMC {
	return &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testBMCName},
	}
}

// newTestBMCClient builds a typed fake client with the VirtualMachineBMC
// status subresource enabled, mirroring the real API server behavior.
func newTestBMCClient(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = bmcv1.AddToScheme(scheme)
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&bmcv1.VirtualMachineBMC{}).
		WithObjects(objects...).
		Build()
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

func findPutSubresourceAction(actions []k8stesting.Action, subresource string) (k8stesting.Action, bool) {
	for _, action := range actions {
		if action.GetVerb() == "put" && action.GetSubresource() == subresource {
			return action, true
		}
	}

	return nil, false
}

func requirePutSubresourceAction(t *testing.T, actions []k8stesting.Action, subresource string) k8stesting.Action {
	t.Helper()

	action, ok := findPutSubresourceAction(actions, subresource)
	if ok {
		return action
	}

	require.Failf(t, "expected PUT subresource action", "subresource %q not found", subresource)
	return nil
}

func requirePutActionOptions[T any](t *testing.T, action k8stesting.Action, subresource string) *T {
	t.Helper()

	putAction, ok := action.(kubevirttesting.PutAction[*T])
	var expectedOptions *T
	require.Truef(
		t,
		ok,
		"expected PUT %q action to have options type %T, got action type %T",
		subresource,
		expectedOptions,
		action,
	)

	return putAction.GetOptions()
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
				WithStorage(testImageSizeBytes).
				WithAnnotation("cdi.kubevirt.io/storage.bind.immediate.requested", "").Build(),
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
				WithStorage(testImageSizeBytes).
				WithAnnotation("cdi.kubevirt.io/storage.bind.immediate.requested", "").Build(),
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
		vmi         *kubevirtv1.VirtualMachineInstance
		expectStart bool
		shouldError bool
	}{
		{
			name:        "Power on a virtual machine that should be on should have no effect",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(true).Build(),
			vmi:         builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			expectStart: false,
			shouldError: false,
		},
		{
			name:        "Power on a virtual machine that should be off should succeed",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(false).Build(),
			expectStart: true,
			shouldError: false,
		},
		{
			name: "Power on a virtual machine whose RunStrategy is set to RerunOnFailure should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).Build(),
			vmi:         builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			expectStart: false,
			shouldError: false,
		},
		{
			name: "Power on a virtual machine whose RunStrategy is set to Halted should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyHalted).Build(),
			expectStart: true,
			shouldError: false,
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

			err := vmrm.PowerOn()
			if tc.shouldError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			var startOptions *kubevirtv1.StartOptions
			if startAction, ok := findPutSubresourceAction(fakeVirtClient.Actions(), "start"); ok {
				startOptions = requirePutActionOptions[kubevirtv1.StartOptions](t, startAction, "start")
			}

			if tc.expectStart {
				require.NotNil(t, startOptions)
				require.False(t, startOptions.Paused)
			} else {
				require.Nil(t, startOptions)
			}
		})
	}
}

func TestVirtualMachineResourceManager_PowerOff(t *testing.T) {
	testCases := []struct {
		name        string
		vm          *kubevirtv1.VirtualMachine
		shouldError bool
	}{
		{
			name:        "Power off a virtual machine that should be on should succeed",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(true).Build(),
			shouldError: false,
		},
		{
			name:        "Power off a virtual machine that should be off should have no effect",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Running(false).Build(),
			shouldError: false,
		},
		{
			name: "Power off a virtual machine whose RunStrategy is set to RerunOnFailure should succeed",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).Build(),
			shouldError: false,
		},
		{
			name: "Power off a virtual machine whose RunStrategy is set to Halted should have no effect",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
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
		})
	}
}

func TestVirtualMachineResourceManager_ForcePowerOff(t *testing.T) {
	testCases := []struct {
		name       string
		vm         *kubevirtv1.VirtualMachine
		vmi        *kubevirtv1.VirtualMachineInstance
		expectStop bool
	}{
		{
			name:       "Force power off a running virtual machine should trigger immediate VM stop",
			vm:         builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			vmi:        builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			expectStop: true,
		},
		{
			name:       "Force power off a halted virtual machine should be a no-op",
			vm:         builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			expectStop: false,
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
			err := vmrm.ForcePowerOff()
			require.NoError(t, err)
			stopAction, ok := findPutSubresourceAction(fakeVirtClient.Actions(), "stop")
			if tc.expectStop {
				require.True(t, ok)
				stopOptions := requirePutActionOptions[kubevirtv1.StopOptions](t, stopAction, "stop")
				require.NotNil(t, stopOptions.GracePeriod)
				require.Zero(t, *stopOptions.GracePeriod)
			} else {
				require.False(t, ok)
			}
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
			name:       "Power cycle a halted virtual machine should trigger VM start",
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

			expectedSubresource := "start"
			if tc.vmi != nil {
				expectedSubresource = "restart"
			}
			requirePutSubresourceAction(t, fakeVirtClient.Actions(), expectedSubresource)
		})
	}
}

func TestVirtualMachineResourceManager_ForcePowerCycle(t *testing.T) {
	testCases := []struct {
		name                string
		vm                  *kubevirtv1.VirtualMachine
		vmi                 *kubevirtv1.VirtualMachineInstance
		expectedSubresource string
	}{
		{
			name:                "Force power cycle a running virtual machine should trigger immediate VM restart",
			vm:                  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			vmi:                 builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			expectedSubresource: "restart",
		},
		{
			name:                "Force power cycle a halted virtual machine should trigger VM start",
			vm:                  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			expectedSubresource: "start",
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

			err := vmrm.ForcePowerCycle()
			require.NoError(t, err)

			powerAction := requirePutSubresourceAction(t, fakeVirtClient.Actions(), tc.expectedSubresource)

			if tc.expectedSubresource == "restart" {
				restartOptions := requirePutActionOptions[kubevirtv1.RestartOptions](t, powerAction, "restart")
				require.NotNil(t, restartOptions.GracePeriodSeconds)
				require.Zero(t, *restartOptions.GracePeriodSeconds)
			}
		})
	}
}

func TestBuildBootOrderPatch(t *testing.T) {
	testCases := []struct {
		name     string
		ordered  []deviceGroup
		expected []map[string]any
	}{
		{
			name:     "empty groups → no operations",
			ordered:  []deviceGroup{},
			expected: []map[string]any{},
		},
		{
			name: "single group, single device → one op with bootOrder=1",
			ordered: []deviceGroup{
				{"disk", "/spec/template/spec/domain/devices/disks", []int{0}},
			},
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/0/bootOrder", "value": uint(1)},
			},
		},
		{
			name: "single group, multiple devices → sequential orders",
			ordered: []deviceGroup{
				{"disk", "/spec/template/spec/domain/devices/disks", []int{0, 2}},
			},
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/0/bootOrder", "value": uint(1)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/2/bootOrder", "value": uint(2)},
			},
		},
		{
			name: "multiple groups → sequential orders across groups",
			ordered: []deviceGroup{
				{"disk", "/spec/template/spec/domain/devices/disks", []int{0, 1}},
				{"interface", "/spec/template/spec/domain/devices/interfaces", []int{0}},
			},
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/0/bootOrder", "value": uint(1)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/1/bootOrder", "value": uint(2)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/interfaces/0/bootOrder", "value": uint(3)},
			},
		},
		{
			name: "group with empty indices → skipped (no ops)",
			ordered: []deviceGroup{
				{"disk", "/spec/template/spec/domain/devices/disks", []int{0}},
				{"interface", "/spec/template/spec/domain/devices/interfaces", []int{}},
				{"disk", "/spec/template/spec/domain/devices/disks", []int{1}},
			},
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/0/bootOrder", "value": uint(1)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/1/bootOrder", "value": uint(2)},
			},
		},
		{
			name: "PXE boot order: interfaces first, then disks, then cdroms",
			ordered: []deviceGroup{
				{"interface", "/spec/template/spec/domain/devices/interfaces", []int{0, 1}},
				{"disk", "/spec/template/spec/domain/devices/disks", []int{0}},
				{"disk", "/spec/template/spec/domain/devices/disks", []int{1}},
			},
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/devices/interfaces/0/bootOrder", "value": uint(1)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/interfaces/1/bootOrder", "value": uint(2)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/0/bootOrder", "value": uint(3)},
				{"op": "add", "path": "/spec/template/spec/domain/devices/disks/1/bootOrder", "value": uint(4)},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := buildBootOrderPatch(tc.ordered)
			require.Equal(t, tc.expected, result)
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
		// --- HDD boot ---
		{
			name:        "HDD: single disk → bootOrder 1",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk", nil).Build(),
			bootDevice:  BootDeviceHdd,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name:        "HDD: multiple disks → sequential orders, disk first",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk-1", nil).WithDisk("disk-2", nil).Build(),
			bootDevice:  BootDeviceHdd,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk-1", util.Ptr[uint](1)).WithDisk("disk-2", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name:        "HDD: disk + interface → disk=1, iface=2",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk", nil).WithInterface("iface", nil).Build(),
			bootDevice:  BootDeviceHdd,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk", util.Ptr[uint](1)).WithInterface("iface", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name: "HDD: disk + cdrom + iface → disk=1, iface=2, cdrom=3 (disks first, then NICs, then CDROMs)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("disk", nil).WithCDRomDisk("cd", nil).WithInterface("iface", nil).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("disk", util.Ptr[uint](1)).WithCDRomDisk("cd", util.Ptr[uint](3)).WithInterface("iface", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name: "HDD: pre-existing boot orders overwritten (disk=2, iface=1 → disk=1, iface=2)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("disk", util.Ptr[uint](2)).WithInterface("iface", util.Ptr[uint](1)).Build(),
			bootDevice: BootDeviceHdd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("disk", util.Ptr[uint](1)).WithInterface("iface", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name:        "HDD: no bootable devices → error",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			bootDevice:  BootDeviceHdd,
			shouldError: true,
		},
		{
			name:        "HDD: only CDROMs → error (no regular disks)",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithCDRomDisk("cd", nil).Build(),
			bootDevice:  BootDeviceHdd,
			shouldError: true,
		},
		{
			name:        "HDD: only interfaces → error (no disks)",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface", nil).Build(),
			bootDevice:  BootDeviceHdd,
			shouldError: true,
		},
		// --- PXE boot ---
		{
			name:        "PXE: single interface → bootOrder 1",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface", nil).Build(),
			bootDevice:  BootDevicePxe,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name:        "PXE: multiple interfaces → sequential orders",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface-1", nil).WithInterface("iface-2", nil).Build(),
			bootDevice:  BootDevicePxe,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface-1", util.Ptr[uint](1)).WithInterface("iface-2", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name:        "PXE: iface + disk → iface=1, disk=2",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface", nil).WithDisk("disk", nil).Build(),
			bootDevice:  BootDevicePxe,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithInterface("iface", util.Ptr[uint](1)).WithDisk("disk", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name: "PXE: iface + disk + cdrom → iface=1, disk=2, cdrom=3",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("iface", nil).WithDisk("disk", nil).WithCDRomDisk("cd", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("iface", util.Ptr[uint](1)).WithDisk("disk", util.Ptr[uint](2)).WithCDRomDisk("cd", util.Ptr[uint](3)).Build(),
			shouldError: false,
		},
		{
			name: "PXE: pre-existing boot orders overwritten (iface=2, disk=1 → iface=1, disk=2)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("iface", util.Ptr[uint](2)).WithDisk("disk", util.Ptr[uint](1)).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("iface", util.Ptr[uint](1)).WithDisk("disk", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name: "PXE: iface + cdrom only (no regular disks) → iface=1, cdrom=2",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("iface", nil).WithCDRomDisk("cd", nil).Build(),
			bootDevice: BootDevicePxe,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithInterface("iface", util.Ptr[uint](1)).WithCDRomDisk("cd", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name:        "PXE: no interfaces → error",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk", nil).Build(),
			bootDevice:  BootDevicePxe,
			shouldError: true,
		},
		{
			name:        "PXE: no devices → error",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			bootDevice:  BootDevicePxe,
			shouldError: true,
		},
		// --- CD-ROM boot ---
		{
			name:        "CD: single cdrom → bootOrder 1",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithCDRomDisk("cd", nil).Build(),
			bootDevice:  BootDeviceCd,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithCDRomDisk("cd", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name:        "CD: cdrom + disk → cdrom=1, disk=2",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithCDRomDisk("cd", nil).WithDisk("disk", nil).Build(),
			bootDevice:  BootDeviceCd,
			expectedVM:  builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithCDRomDisk("cd", util.Ptr[uint](1)).WithDisk("disk", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name: "CD: cdrom + disk + iface → cdrom=1, disk=2, iface=3",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithCDRomDisk("cd", nil).WithDisk("disk", nil).WithInterface("iface", nil).Build(),
			bootDevice: BootDeviceCd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithCDRomDisk("cd", util.Ptr[uint](1)).WithDisk("disk", util.Ptr[uint](2)).WithInterface("iface", util.Ptr[uint](3)).Build(),
			shouldError: false,
		},
		{
			name: "CD: pre-existing boot orders overwritten (disk=1, cdrom=nil → cdrom=1, disk=2)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("disk", util.Ptr[uint](1)).WithCDRomDisk("cd", nil).Build(),
			bootDevice: BootDeviceCd,
			// Device order is unchanged by Patch; only bootOrder values change.
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithDisk("disk", util.Ptr[uint](2)).WithCDRomDisk("cd", util.Ptr[uint](1)).Build(),
			shouldError: false,
		},
		{
			name: "CD: multiple cdroms + disk + iface → cdrom1=1, cdrom2=2, disk=3, iface=4",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithCDRomDisk("cd1", nil).WithCDRomDisk("cd2", nil).
				WithDisk("disk", nil).WithInterface("iface", nil).Build(),
			bootDevice: BootDeviceCd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithCDRomDisk("cd1", util.Ptr[uint](1)).WithCDRomDisk("cd2", util.Ptr[uint](2)).
				WithDisk("disk", util.Ptr[uint](3)).WithInterface("iface", util.Ptr[uint](4)).Build(),
			shouldError: false,
		},
		{
			name: "CD: cdrom + iface only (no regular disks) → cdrom=1, iface=2",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithCDRomDisk("cd", nil).WithInterface("iface", nil).Build(),
			bootDevice: BootDeviceCd,
			expectedVM: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				WithCDRomDisk("cd", util.Ptr[uint](1)).WithInterface("iface", util.Ptr[uint](2)).Build(),
			shouldError: false,
		},
		{
			name:        "CD: no cdrom → error",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithDisk("disk", nil).Build(),
			bootDevice:  BootDeviceCd,
			shouldError: true,
		},
		{
			name:        "CD: no devices → error",
			vm:          builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			bootDevice:  BootDeviceCd,
			shouldError: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)
			fakeBMCClient := newTestBMCClient(newTestBMC())

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				bmcClient:  fakeBMCClient,
				namespace:  testNamespace,
				name:       testVMName,
				bmcName:    testBMCName,
			}

			err := vmrm.SetBootDevice(tc.bootDevice, &BootOptions{Mode: BootModePersistent})
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

func TestVirtualMachineResourceManager_BootOverrideStatus(t *testing.T) {
	fakeBMCClient := newTestBMCClient(newTestBMC())
	vmrm := &VirtualMachineResourceManager{
		ctx:       context.TODO(),
		bmcClient: fakeBMCClient,
		namespace: testNamespace,
		bmcName:   testBMCName,
	}

	override := &bmcv1.BootOverrideStatus{
		Mode:       bmcv1.BootOverrideModeOneshot,
		VMIUID:     "test-uid",
		BootOrders: map[string]uint{"disk:root": 1, "interface:default": 0},
	}
	require.NoError(t, vmrm.saveBootOverride(override))

	saved, err := vmrm.GetBootOverride()
	require.NoError(t, err)
	require.Equal(t, override.Mode, saved.Mode)
	require.Equal(t, override.VMIUID, saved.VMIUID)
	require.Equal(t, override.BootOrders, saved.BootOrders)

	require.NoError(t, vmrm.clearBootOverride())
	saved, err = vmrm.GetBootOverride()
	require.NoError(t, err)
	require.Nil(t, saved)
}

func TestVirtualMachineResourceManager_SetBootDevicePersistentSavesOverrideMarker(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("disk", nil).
		WithInterface("iface", nil).
		Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)

	fakeBMCClient := newTestBMCClient(newTestBMC())

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		bmcClient:  fakeBMCClient,
		namespace:  testNamespace,
		name:       testVMName,
		bmcName:    testBMCName,
	}

	require.NoError(t, vmrm.saveBootOverride(&bmcv1.BootOverrideStatus{
		Mode:       bmcv1.BootOverrideModeOneshot,
		BootOrders: map[string]uint{"disk:disk": 1},
	}))
	saved, err := vmrm.GetBootOverride()
	require.NoError(t, err)
	require.NotNil(t, saved)

	err = vmrm.SetBootDevice(BootDevicePxe, &BootOptions{Mode: BootModePersistent})
	require.NoError(t, err)

	// Persistent override overwrites the oneshot backup with a marker.
	saved, err = vmrm.GetBootOverride()
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, bmcv1.BootOverrideModePersistent, saved.Mode)
	require.Nil(t, saved.BootOrders)
}

func TestVirtualMachineResourceManager_OneshotOverwritesPersistentMarker(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("disk", nil).
		WithInterface("iface", nil).
		Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)

	fakeBMCClient := newTestBMCClient(newTestBMC())

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		bmcClient:  fakeBMCClient,
		namespace:  testNamespace,
		name:       testVMName,
		bmcName:    testBMCName,
	}

	require.NoError(t, vmrm.saveBootOverride(&bmcv1.BootOverrideStatus{
		Mode: bmcv1.BootOverrideModePersistent,
	}))
	saved, err := vmrm.GetBootOverride()
	require.NoError(t, err)
	require.Equal(t, bmcv1.BootOverrideModePersistent, saved.Mode)

	// A oneshot overwrites the persistent marker with a fresh backup.
	err = vmrm.SetBootDevice(BootDevicePxe, &BootOptions{Mode: BootModeOneshot})
	require.NoError(t, err)

	saved, err = vmrm.GetBootOverride()
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, bmcv1.BootOverrideModeOneshot, saved.Mode)
	require.NotNil(t, saved.BootOrders)
	require.Contains(t, saved.BootOrders, "disk:disk")
	require.Contains(t, saved.BootOrders, "interface:iface")
}

func TestVirtualMachineResourceManager_SetBootDeviceAllowsFirmwareTemplateChangeOnRunningVM(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("disk", nil).
		WithInterface("iface", nil).
		Ready(true).
		Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)
	fakeBMCClient := newTestBMCClient(newTestBMC())

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		bmcClient:  fakeBMCClient,
		namespace:  testNamespace,
		name:       testVMName,
		bmcName:    testBMCName,
	}

	err := vmrm.SetBootDevice(BootDevicePxe, &BootOptions{Mode: BootModeOneshot, EFIBoot: util.Ptr(true)})
	require.NoError(t, err)

	saved, err := vmrm.GetBootOverride()
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Empty(t, saved.VMIUID, "VMIUID should be empty because no VMI exists in the test") // VM was not running
	require.Equal(t, bmcv1.FirmwareTypeLegacy, saved.OriginalFirmware)
	require.Contains(t, saved.BootOrders, "disk:disk")
	require.Zero(t, saved.BootOrders["disk:disk"], "0 means device existed without bootOrder")
	require.Contains(t, saved.BootOrders, "interface:iface")
	require.Zero(t, saved.BootOrders["interface:iface"], "0 means device existed without bootOrder")

	updatedVM, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI.SecureBoot)
	require.False(t, *updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI.SecureBoot)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Devices.Disks[0].BootOrder)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Devices.Interfaces[0].BootOrder)
}

func TestVirtualMachineResourceManager_DoubleOneshotPreservesOriginalBackup(t *testing.T) {
	// Two back-to-back oneshots before reboot must keep the ORIGINAL
	// backup, not the intermediate state left by the first oneshot.
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("disk", util.Ptr[uint](1)).
		WithCDRomDisk("cdrom", util.Ptr[uint](3)).
		WithInterface("iface", util.Ptr[uint](2)).
		Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)
	fakeBMCClient := newTestBMCClient(newTestBMC())

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		bmcClient:  fakeBMCClient,
		namespace:  testNamespace,
		name:       testVMName,
		bmcName:    testBMCName,
	}

	err := vmrm.SetBootDevice(BootDevicePxe, &BootOptions{Mode: BootModeOneshot})
	require.NoError(t, err)

	backup, err := vmrm.GetBootOverride()
	require.NoError(t, err)
	require.NotNil(t, backup)
	require.Equal(t, uint(1), backup.BootOrders["disk:disk"])
	require.Equal(t, uint(2), backup.BootOrders["interface:iface"])
	require.Equal(t, uint(3), backup.BootOrders["cdrom:cdrom"])

	updatedVM, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	disks := updatedVM.Spec.Template.Spec.Domain.Devices.Disks
	ifaces := updatedVM.Spec.Template.Spec.Domain.Devices.Interfaces
	require.Equal(t, util.Ptr[uint](2), disks[0].BootOrder)  // disk
	require.Equal(t, util.Ptr[uint](3), disks[1].BootOrder)  // cdrom
	require.Equal(t, util.Ptr[uint](1), ifaces[0].BootOrder) // iface (PXE first)

	err = vmrm.SetBootDevice(BootDeviceCd, &BootOptions{Mode: BootModeOneshot})
	require.NoError(t, err)

	// The backup must still hold the original order, not the PXE state.
	backup, err = vmrm.GetBootOverride()
	require.NoError(t, err)
	require.NotNil(t, backup)
	require.Equal(t, uint(1), backup.BootOrders["disk:disk"],
		"original disk boot order must be preserved")
	require.Equal(t, uint(2), backup.BootOrders["interface:iface"],
		"original iface boot order must be preserved")
	require.Equal(t, uint(3), backup.BootOrders["cdrom:cdrom"],
		"original cdrom boot order must be preserved")

	updatedVM, err = fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	disks = updatedVM.Spec.Template.Spec.Domain.Devices.Disks
	ifaces = updatedVM.Spec.Template.Spec.Domain.Devices.Interfaces
	require.Equal(t, util.Ptr[uint](2), disks[0].BootOrder)  // disk
	require.Equal(t, util.Ptr[uint](1), disks[1].BootOrder)  // cdrom (CDROM first)
	require.Equal(t, util.Ptr[uint](3), ifaces[0].BootOrder) // iface
}

func TestVirtualMachineResourceManager_DoubleOneshotRestoresLateFirmwareChange(t *testing.T) {
	// The firmware change arrives in the SECOND oneshot: the backup taken
	// on the first must already hold the original firmware, or it would
	// never be restored.
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("disk", util.Ptr[uint](1)).
		WithCDRomDisk("cdrom", util.Ptr[uint](2)).
		WithInterface("iface", util.Ptr[uint](3)).
		Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)
	fakeBMCClient := newTestBMCClient(newTestBMC())

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		bmcClient:  fakeBMCClient,
		namespace:  testNamespace,
		name:       testVMName,
		bmcName:    testBMCName,
	}

	require.NoError(t, vmrm.SetBootDevice(BootDevicePxe, &BootOptions{Mode: BootModeOneshot}))
	backup, err := vmrm.GetBootOverride()
	require.NoError(t, err)
	require.Equal(t, bmcv1.FirmwareTypeLegacy, backup.OriginalFirmware,
		"firmware must be recorded even when the first oneshot does not change it")

	require.NoError(t, vmrm.SetBootDevice(BootDeviceCd, &BootOptions{Mode: BootModeOneshot, EFIBoot: util.Ptr(true)}))

	updatedVM, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware)

	backup, err = vmrm.GetBootOverride()
	require.NoError(t, err)
	require.Equal(t, bmcv1.FirmwareTypeLegacy, backup.OriginalFirmware,
		"original BIOS firmware must survive the second oneshot")

	// Restore against the EFI-switched VM must patch firmware back to BIOS.
	ops := BuildBootOrderRestorePatch(updatedVM, backup)
	require.Contains(t, ops,
		map[string]any{"op": "remove", "path": "/spec/template/spec/domain/firmware/bootloader/efi"})
}

func TestBuildBootOrderRestorePatch_SkipsUnchangedFirmware(t *testing.T) {
	// No oneshot changed firmware: restore must not materialize an
	// explicit firmware section on a VM that had firmware unset.
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("disk", util.Ptr[uint](2)).
		Build()
	backup := &bmcv1.BootOverrideStatus{
		Mode:             bmcv1.BootOverrideModeOneshot,
		BootOrders:       map[string]uint{"disk:disk": 1},
		OriginalFirmware: bmcv1.FirmwareTypeLegacy,
	}

	for _, op := range BuildBootOrderRestorePatch(vm, backup) {
		require.NotContains(t, op["path"], "firmware")
	}
}

func TestVirtualMachineResourceManager_SetFirmwareMode(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		namespace:  testNamespace,
		name:       testVMName,
	}

	err := vmrm.SetFirmwareMode(FirmwareModeUEFI)
	require.NoError(t, err)

	updatedVM, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI.SecureBoot)
	require.False(t, *updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI.SecureBoot)

	err = vmrm.SetFirmwareMode(FirmwareModeLegacy)
	require.NoError(t, err)

	updatedVM, err = fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.BIOS)
	require.Nil(t, updatedVM.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI)
}

func TestBuildFirmwarePatch(t *testing.T) {
	testCases := []struct {
		name     string
		firmware *kubevirtv1.Firmware
		smm      *kubevirtv1.FeatureState
		efi      bool
		expected []map[string]any
	}{
		{
			name:     "nil firmware to EFI",
			firmware: nil,
			efi:      true,
			expected: []map[string]any{
				{
					"op":   "add",
					"path": "/spec/template/spec/domain/firmware",
					"value": map[string]any{
						"bootloader": map[string]any{"efi": map[string]any{"secureBoot": false}},
					},
				},
			},
		},
		{
			name:     "nil firmware to EFI with SMM enabled",
			firmware: nil,
			smm:      &kubevirtv1.FeatureState{},
			efi:      true,
			expected: []map[string]any{
				{
					"op":   "add",
					"path": "/spec/template/spec/domain/firmware",
					"value": map[string]any{
						"bootloader": map[string]any{"efi": map[string]any{"secureBoot": true}},
					},
				},
			},
		},
		{
			name:     "nil firmware to BIOS",
			firmware: nil,
			efi:      false,
			expected: []map[string]any{
				{
					"op":   "add",
					"path": "/spec/template/spec/domain/firmware",
					"value": map[string]any{
						"bootloader": map[string]any{"bios": map[string]any{}},
					},
				},
			},
		},
		{
			name:     "nil bootloader to EFI",
			firmware: &kubevirtv1.Firmware{},
			efi:      true,
			expected: []map[string]any{
				{
					"op":    "add",
					"path":  "/spec/template/spec/domain/firmware/bootloader",
					"value": map[string]any{"efi": map[string]any{"secureBoot": false}},
				},
			},
		},
		{
			name: "already EFI",
			firmware: &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{EFI: &kubevirtv1.EFI{}},
			},
			efi:      true,
			expected: nil,
		},
		{
			name: "already BIOS",
			firmware: &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{BIOS: &kubevirtv1.BIOS{}},
			},
			efi:      false,
			expected: nil,
		},
		{
			name: "BIOS to EFI",
			firmware: &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{BIOS: &kubevirtv1.BIOS{}},
			},
			efi: true,
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/firmware/bootloader/efi", "value": map[string]any{"secureBoot": false}},
				{"op": "remove", "path": "/spec/template/spec/domain/firmware/bootloader/bios"},
			},
		},
		{
			name: "BIOS to EFI with SMM explicitly enabled",
			firmware: &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{BIOS: &kubevirtv1.BIOS{}},
			},
			smm: &kubevirtv1.FeatureState{Enabled: util.Ptr(true)},
			efi: true,
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/firmware/bootloader/efi", "value": map[string]any{"secureBoot": true}},
				{"op": "remove", "path": "/spec/template/spec/domain/firmware/bootloader/bios"},
			},
		},
		{
			name: "BIOS to EFI with SMM explicitly disabled",
			firmware: &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{BIOS: &kubevirtv1.BIOS{}},
			},
			smm: &kubevirtv1.FeatureState{Enabled: util.Ptr(false)},
			efi: true,
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/firmware/bootloader/efi", "value": map[string]any{"secureBoot": false}},
				{"op": "remove", "path": "/spec/template/spec/domain/firmware/bootloader/bios"},
			},
		},
		{
			name: "EFI to BIOS",
			firmware: &kubevirtv1.Firmware{
				Bootloader: &kubevirtv1.Bootloader{EFI: &kubevirtv1.EFI{}},
			},
			efi: false,
			expected: []map[string]any{
				{"op": "add", "path": "/spec/template/spec/domain/firmware/bootloader/bios", "value": map[string]any{}},
				{"op": "remove", "path": "/spec/template/spec/domain/firmware/bootloader/efi"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).WithTemplate().Build()
			vm.Spec.Template.Spec.Domain.Firmware = tc.firmware
			if tc.smm != nil {
				vm.Spec.Template.Spec.Domain.Features = &kubevirtv1.Features{SMM: tc.smm}
			}

			require.Equal(t, tc.expected, BuildFirmwarePatch(vm, tc.efi))
		})
	}
}

func TestBuildBootOrderRestorePatch_LeavesAddedDevicesAndClearsConflictingSavedOrder(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithDisk("root", util.Ptr[uint](2)).
		WithDisk("new", util.Ptr[uint](1)).
		WithInterface("default", util.Ptr[uint](3)).
		Build()
	backup := &bmcv1.BootOverrideStatus{
		Mode: bmcv1.BootOverrideModeOneshot,
		BootOrders: map[string]uint{
			"disk:root":         1,
			"interface:default": 0,
		},
	}

	require.Equal(t, []map[string]any{
		{"op": "remove", "path": "/spec/template/spec/domain/devices/disks/0/bootOrder"},
		{"op": "remove", "path": "/spec/template/spec/domain/devices/interfaces/0/bootOrder"},
	}, BuildBootOrderRestorePatch(vm, backup))
}

func TestVirtualMachineResourceManager_ClearBootOverrides_WithBackup(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithInterface("default", util.Ptr[uint](1)).
		WithDisk("containerdisk", util.Ptr[uint](2)).
		WithCDRomDisk("cdrom", util.Ptr[uint](3)).
		Build()

	bmc := newTestBMC()
	bmc.Status.BootOverride = &bmcv1.BootOverrideStatus{
		Mode: bmcv1.BootOverrideModeOneshot,
		BootOrders: map[string]uint{
			"interface:default":  0,
			"disk:containerdisk": 0,
			"cdrom:cdrom":        0,
		},
	}

	fakeBMCClient := newTestBMCClient(bmc)
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)

	vmrm := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: fakeVirtClient,
		bmcClient:  fakeBMCClient,
		namespace:  testNamespace,
		name:       testVMName,
		bmcName:    testBMCName,
	}

	err := vmrm.ClearBootOverrides()
	require.NoError(t, err)

	updatedVM, err := fakeVirtClient.KubevirtV1().VirtualMachines(testNamespace).
		Get(context.TODO(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	for _, d := range updatedVM.Spec.Template.Spec.Domain.Devices.Disks {
		require.Nil(t, d.BootOrder, "disk %s bootOrder should be nil", d.Name)
	}
	for _, iface := range updatedVM.Spec.Template.Spec.Domain.Devices.Interfaces {
		require.Nil(t, iface.BootOrder, "interface %s bootOrder should be nil", iface.Name)
	}
	updatedBMC := &bmcv1.VirtualMachineBMC{}
	err = fakeBMCClient.Get(context.TODO(), client.ObjectKey{Namespace: testNamespace, Name: testBMCName}, updatedBMC)
	require.NoError(t, err)
	require.Nil(t, updatedBMC.Status.BootOverride, "status.bootOverride should be cleared")
}
