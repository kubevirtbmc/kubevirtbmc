package resourcemanager

import (
	"context"
	"errors"
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
		name          string
		vm            *kubevirtv1.VirtualMachine
		vmi           *kubevirtv1.VirtualMachineInstance // optional; present when StartRequest already consumed
		startErr      string                             // if non-empty, inject error on "start" subresource
		getErr        bool                               // if true, inject error on VM Get (verify step)
		wantErr       bool
		wantRetryable bool
		wantStart     bool
	}{
		{
			name:      "Power on a not-ready VM: Start succeeds → nil",
			vm:        builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			wantStart: true,
		},
		{
			name: "Power on a Ready VM with Start error → nil (idempotent success)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				RunStrategy(kubevirtv1.RunStrategyAlways).
				Build(),
			startErr:  "VM is already running",
			wantStart: true,
		},
		{
			name: "Power on a Ready VM with Start error and RunStrategy=Halted → ErrRetryable (Stop→Start race)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				RunStrategy(kubevirtv1.RunStrategyHalted).
				Build(),
			startErr:      "VM is already running",
			wantErr:       true,
			wantRetryable: true,
			wantStart:     true,
		},
		{
			name:          "Power on a not-Ready VM with Start error → ErrRetryable (transitional state)",
			vm:            builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(false).Build(),
			startErr:      "VM is already running",
			wantErr:       true,
			wantRetryable: true,
			wantStart:     true,
		},
		{
			name:      "Power on when Get verify also fails → wrapped error, not retryable",
			vm:        builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(false).Build(),
			startErr:  "VM is already running",
			getErr:    true,
			wantErr:   true,
			wantStart: true,
		},
		{
			name: "Power on a not-Ready VM with Start error and RunStrategy=Always → nil (idempotent, VMI starting)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				Running(true).
				Build(),
			startErr:  "VM is already running",
			wantStart: true,
		},
		{
			name: "Power on a Ready Manual VM with pending StopRequest → ErrRetryable (stop in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				RunStrategy(kubevirtv1.RunStrategyManual).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StopRequest}).
				Build(),
			startErr:      "VM is already running",
			wantErr:       true,
			wantRetryable: true,
			wantStart:     true,
		},
		{
			name: "Power on a not-Ready Manual VM with pending StartRequest → nil (on→on idempotent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyManual).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest}).
				Build(),
			startErr:  "unable to complete request: stop/start already underway",
			wantStart: true,
		},
		{
			name: "Power on a Ready RerunOnFailure VM with pending StopRequest → ErrRetryable (stop in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StopRequest}).
				Build(),
			startErr:      "VM is already running",
			wantErr:       true,
			wantRetryable: true,
			wantStart:     true,
		},
		{
			name: "Power on a not-Ready RerunOnFailure VM with pending StartRequest → nil (on→on idempotent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest}).
				Build(),
			startErr:  "unable to complete request: stop/start already underway",
			wantStart: true,
		},
		{
			name: "Power on a not-Ready Manual VM with starting VMI (StartRequest consumed) → nil (on→on idempotent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyManual).
				Build(),
			vmi: builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).
				Phase(kubevirtv1.Scheduling).
				Build(),
			startErr:  "VM is already running",
			wantStart: true,
		},
		{
			name: "Power on a not-Ready RerunOnFailure VM with starting VMI (StartRequest consumed) → nil (on→on idempotent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				Build(),
			vmi: builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).
				Phase(kubevirtv1.Pending).
				Build(),
			startErr:  "VM is already running",
			wantStart: true,
		},
		{
			name: "Power on a RerunOnFailure VM with Failed VMI → ErrRetryable (not startable)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				Build(),
			vmi: builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).
				Phase(kubevirtv1.Failed).
				Build(),
			startErr:      "RerunOnFailure does not support starting VM from failed state",
			wantErr:       true,
			wantRetryable: true,
			wantStart:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []runtime.Object{tc.vm}
			if tc.vmi != nil {
				objs = append(objs, tc.vmi)
			}
			fakeVirtClient := kubevirtfake.NewSimpleClientset(objs...)
			if tc.startErr != "" {
				injectSubresourceError(t, fakeVirtClient, "start", tc.startErr)
			}
			if tc.getErr {
				injectGetError(t, fakeVirtClient, "virtualmachines")
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.PowerOn()
			if tc.wantErr {
				require.Error(t, err)
				var retryable *ErrRetryable
				require.Equal(t, tc.wantRetryable, errors.As(err, &retryable),
					"retryable mismatch for err=%v", err)
			} else {
				require.NoError(t, err)
			}

			_, hasStart := findPutSubresourceAction(fakeVirtClient.Actions(), "start")
			require.Equal(t, tc.wantStart, hasStart, "start subresource action expectation")
		})
	}
}

func TestVirtualMachineResourceManager_PowerOff(t *testing.T) {
	testCases := []struct {
		name          string
		vm            *kubevirtv1.VirtualMachine
		stopErr       string // if non-empty, inject error on "stop" subresource
		getErr        bool   // if true, inject error on VM Get (verify step)
		wantErr       bool
		wantRetryable bool
		wantStop      bool
	}{
		{
			name:     "Power off a Running VM: Stop succeeds → nil",
			vm:       builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			wantStop: true,
		},
		{
			name:     "Power off a not-Ready VM with Stop error → nil (idempotent success)",
			vm:       builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(false).Build(),
			stopErr:  "VM is not running",
			wantStop: true,
		},
		{
			name:          "Power off a Ready VM with Stop error → ErrRetryable (transitional state)",
			vm:            builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			stopErr:       "VM is not running",
			wantErr:       true,
			wantRetryable: true,
			wantStop:      true,
		},
		{
			name:     "Power off when Get verify also fails → wrapped error, not retryable",
			vm:       builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			stopErr:  "VM is not running",
			getErr:   true,
			wantErr:  true,
			wantStop: true,
		},
		{
			name: "Power off a stopped Manual VM → nil (idempotent, no start intent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyManual).
				Build(),
			stopErr:  "VM is not running",
			wantStop: true,
		},
		{
			name: "Power off a stopped Manual VM with pending StartRequest → ErrRetryable (start in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyManual).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest}).
				Build(),
			stopErr:       "VM is not running",
			wantErr:       true,
			wantRetryable: true,
			wantStop:      true,
		},
		{
			name: "Power off a stopped RerunOnFailure VM → nil (idempotent, no start intent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				Build(),
			stopErr:  "VM is not running",
			wantStop: true,
		},
		{
			name: "Power off a stopped RerunOnFailure VM with pending StartRequest → ErrRetryable (start in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest}).
				Build(),
			stopErr:       "VM is not running",
			wantErr:       true,
			wantRetryable: true,
			wantStop:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)
			if tc.stopErr != "" {
				injectSubresourceError(t, fakeVirtClient, "stop", tc.stopErr)
			}
			if tc.getErr {
				injectGetError(t, fakeVirtClient, "virtualmachines")
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.PowerOff()
			if tc.wantErr {
				require.Error(t, err)
				var retryable *ErrRetryable
				require.Equal(t, tc.wantRetryable, errors.As(err, &retryable),
					"retryable mismatch for err=%v", err)
			} else {
				require.NoError(t, err)
			}

			_, hasStop := findPutSubresourceAction(fakeVirtClient.Actions(), "stop")
			require.Equal(t, tc.wantStop, hasStop, "stop subresource action expectation")
		})
	}
}

func TestVirtualMachineResourceManager_ForcePowerOff(t *testing.T) {
	testCases := []struct {
		name          string
		vm            *kubevirtv1.VirtualMachine
		stopErr       string
		wantErr       bool
		wantRetryable bool
		wantStop      bool
	}{
		{
			name:     "Force power off a Ready VM: Stop succeeds → nil with gracePeriod=0",
			vm:       builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			wantStop: true,
		},
		{
			name:     "Force power off a not-Ready VM with Stop error → nil (idempotent success)",
			vm:       builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(false).Build(),
			stopErr:  "VM is not running",
			wantStop: true,
		},
		{
			name:          "Force power off a Ready VM with Stop error → ErrRetryable (transitional state)",
			vm:            builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			stopErr:       "VM is not running",
			wantErr:       true,
			wantRetryable: true,
			wantStop:      true,
		},
		{
			name: "Force power off a stopped Manual VM → nil (idempotent, no start intent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyManual).
				Build(),
			stopErr:  "VM is not running",
			wantStop: true,
		},
		{
			name: "Force power off a stopped Manual VM with pending StartRequest → ErrRetryable (start in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyManual).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest}).
				Build(),
			stopErr:       "VM is not running",
			wantErr:       true,
			wantRetryable: true,
			wantStop:      true,
		},
		{
			name: "Force power off a stopped RerunOnFailure VM → nil (idempotent, no start intent)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				Build(),
			stopErr:  "VM is not running",
			wantStop: true,
		},
		{
			name: "Force power off a stopped RerunOnFailure VM with pending StartRequest → ErrRetryable (start in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyRerunOnFailure).
				WithStateChangeRequests(kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest}).
				Build(),
			stopErr:       "VM is not running",
			wantErr:       true,
			wantRetryable: true,
			wantStop:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVirtClient := kubevirtfake.NewSimpleClientset(tc.vm)
			if tc.stopErr != "" {
				injectSubresourceError(t, fakeVirtClient, "stop", tc.stopErr)
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.ForcePowerOff()
			if tc.wantErr {
				require.Error(t, err)
				var retryable *ErrRetryable
				require.Equal(t, tc.wantRetryable, errors.As(err, &retryable),
					"retryable mismatch for err=%v", err)
			} else {
				require.NoError(t, err)
			}

			stopAction, hasStop := findPutSubresourceAction(fakeVirtClient.Actions(), "stop")
			require.Equal(t, tc.wantStop, hasStop, "stop subresource action expectation")
			if hasStop {
				stopOptions := requirePutActionOptions[kubevirtv1.StopOptions](t, stopAction, "stop")
				require.NotNil(t, stopOptions.GracePeriod)
				require.Zero(t, *stopOptions.GracePeriod, "ForcePowerOff should use gracePeriod=0")
			}
		})
	}
}

// injectSubresourceError makes the fake client return errMsg for PUTs to the
// given VM subresource (e.g. "start", "stop").
func injectSubresourceError(t *testing.T, c *kubevirtfake.Clientset, subresource, errMsg string) {
	t.Helper()
	c.PrependReactor("put", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == subresource {
			return true, nil, errors.New(errMsg)
		}
		return false, nil, nil
	})
}

// injectGetError prepends a reactor that fails VM Get calls, simulating a
// verify-state failure after the primary operation has already errored.
func injectGetError(t *testing.T, c *kubevirtfake.Clientset, resource string) {
	t.Helper()
	c.PrependReactor("get", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("verify get failed")
	})
}

func TestVirtualMachineResourceManager_PowerCycle(t *testing.T) {
	testCases := []struct {
		name                string
		vm                  *kubevirtv1.VirtualMachine
		vmi                 *kubevirtv1.VirtualMachineInstance
		restartErr          string
		wantErr             bool
		wantRetryable       bool
		expectedSubresource string
	}{
		{
			name:                "Power cycle a running virtual machine should trigger VM restart",
			vm:                  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build(),
			vmi:                 builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			expectedSubresource: "restart",
		},
		{
			name: "Power cycle while VMI exists but Ready=false should still restart (startup race)",
			// Soft→on→reset race: VMI is already present/starting while
			// VM.Status.Ready is still false. Falling back to PowerOn is a
			// silent no-op under RunStrategyAlways and swallows the reset.
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyAlways).
				Build(),
			vmi:                 builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			expectedSubresource: "restart",
		},
		{
			name:                "Power cycle a halted virtual machine should trigger VM start",
			vm:                  builder.NewVirtualMachineBuilder(testNamespace, testVMName).Build(),
			vmi:                 nil,
			expectedSubresource: "start",
		},
		{
			name: "Restart fails with Stop+Start queued → nil (idempotent restart underway)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				WithStateChangeRequests(
					kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StopRequest},
					kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StartRequest},
				).
				Build(),
			vmi:                 builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			restartErr:          "unable to complete request: stop/start already underway",
			expectedSubresource: "restart",
		},
		{
			name: "Restart fails with only StopRequest → ErrRetryable (soft-off in progress)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				WithStateChangeRequests(
					kubevirtv1.VirtualMachineStateChangeRequest{Action: kubevirtv1.StopRequest},
				).
				Build(),
			vmi:                 builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).Build(),
			restartErr:          "unable to complete request: stop/start already underway",
			wantErr:             true,
			wantRetryable:       true,
			expectedSubresource: "restart",
		},
		{
			name: "Restart fails while non-final VMI present → ErrRetryable (transitional)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				Build(),
			vmi: builder.NewVirtualMachineInstanceBuilder(testNamespace, testVMName).
				Phase(kubevirtv1.Running).
				Build(),
			restartErr:          "conflict",
			wantErr:             true,
			wantRetryable:       true,
			expectedSubresource: "restart",
		},
		{
			name: "Restart fails after VMI gone → fall back to PowerOn",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(true).
				Build(),
			vmi:                 nil,
			restartErr:          "VM is not running",
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
			if tc.restartErr != "" {
				injectSubresourceError(t, fakeVirtClient, "restart", tc.restartErr)
			}

			vmrm := &VirtualMachineResourceManager{
				ctx:        context.TODO(),
				virtClient: fakeVirtClient,
				namespace:  testNamespace,
				name:       testVMName,
			}

			err := vmrm.PowerCycle()
			if tc.wantErr {
				require.Error(t, err)
				var retryable *ErrRetryable
				require.Equal(t, tc.wantRetryable, errors.As(err, &retryable))
			} else {
				require.NoError(t, err)
			}
			requirePutSubresourceAction(t, fakeVirtClient.Actions(), tc.expectedSubresource)
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
			name: "Force power cycle while VMI exists but Ready=false should still restart (startup race)",
			vm: builder.NewVirtualMachineBuilder(testNamespace, testVMName).
				Ready(false).
				RunStrategy(kubevirtv1.RunStrategyAlways).
				Build(),
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
