package resourcemanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cdifake "kubevirt.io/client-go/containerizeddataimporter/fake"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
	"kubevirt.io/kubevirtbmc/pkg/builder"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

func TestStaticVirtualMediaConfigSource(t *testing.T) {
	want := VirtualMediaConfig{
		StorageClass:       "fast-sc",
		VolumeMode:         util.Ptr(corev1.PersistentVolumeBlock),
		SizeMarginPercent:  30,
		InsecureSkipVerify: true,
		CABundleConfigMap:  "custom-ca",
	}
	cfg, err := NewStaticVirtualMediaConfigSource(want).Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, cfg)
}

func TestClusterVirtualMediaConfigSource(t *testing.T) {
	newBMC := func(mutate func(*bmcv1.VirtualMachineBMC)) *bmcv1.VirtualMachineBMC {
		bmc := &bmcv1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testBMCName},
		}
		if mutate != nil {
			mutate(bmc)
		}
		return bmc
	}
	withFullSpec := func(bmc *bmcv1.VirtualMachineBMC) {
		bmc.Annotations = map[string]string{bmcv1.AnnotationDataVolumeSizeMargin: "30"}
		bmc.Spec.Redfish = &bmcv1.RedfishSpec{
			VirtualMedia: &bmcv1.VirtualMediaSpec{
				Storage: &bmcv1.VirtualMediaStorageSpec{
					StorageClassName: util.Ptr("fast-sc"),
					VolumeMode:       util.Ptr(corev1.PersistentVolumeBlock),
				},
				TLS: &bmcv1.VirtualMediaTLSSpec{
					InsecureSkipVerify:   util.Ptr(true),
					CABundleConfigMapRef: &corev1.LocalObjectReference{Name: "custom-ca"},
				},
			},
		}
	}

	t.Run("translates the full virtual media spec", func(t *testing.T) {
		source := NewClusterVirtualMediaConfigSource(newTestBMCClient(newBMC(withFullSpec)), testNamespace, testBMCName)
		cfg, err := source.Resolve(context.Background())
		require.NoError(t, err)
		require.Equal(t, VirtualMediaConfig{
			StorageClass:       "fast-sc",
			VolumeMode:         util.Ptr(corev1.PersistentVolumeBlock),
			SizeMarginPercent:  30,
			InsecureSkipVerify: true,
			CABundleConfigMap:  "custom-ca",
		}, cfg)
	})

	t.Run("empty spec resolves to the zero config", func(t *testing.T) {
		source := NewClusterVirtualMediaConfigSource(newTestBMCClient(newBMC(nil)), testNamespace, testBMCName)
		cfg, err := source.Resolve(context.Background())
		require.NoError(t, err)
		require.Equal(t, VirtualMediaConfig{}, cfg)
	})

	for _, margin := range []string{"invalid", "0", "-5"} {
		t.Run("invalid size margin "+margin+" is treated as unset", func(t *testing.T) {
			bmc := newBMC(func(bmc *bmcv1.VirtualMachineBMC) {
				bmc.Annotations = map[string]string{bmcv1.AnnotationDataVolumeSizeMargin: margin}
			})
			source := NewClusterVirtualMediaConfigSource(newTestBMCClient(bmc), testNamespace, testBMCName)
			cfg, err := source.Resolve(context.Background())
			require.NoError(t, err)
			require.Zero(t, cfg.SizeMarginPercent)
		})
	}

	t.Run("missing VirtualMachineBMC returns an error", func(t *testing.T) {
		source := NewClusterVirtualMediaConfigSource(newTestBMCClient(), testNamespace, testBMCName)
		_, err := source.Resolve(context.Background())
		require.Error(t, err)
	})

	t.Run("resolve reflects spec changes without restarting the agent", func(t *testing.T) {
		bmcClient := newTestBMCClient(newBMC(nil))
		source := NewClusterVirtualMediaConfigSource(bmcClient, testNamespace, testBMCName)

		cfg, err := source.Resolve(context.Background())
		require.NoError(t, err)
		require.Empty(t, cfg.StorageClass)

		bmc := &bmcv1.VirtualMachineBMC{}
		require.NoError(t, bmcClient.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testBMCName}, bmc))
		withFullSpec(bmc)
		require.NoError(t, bmcClient.Update(context.Background(), bmc))

		cfg, err = source.Resolve(context.Background())
		require.NoError(t, err)
		require.Equal(t, "fast-sc", cfg.StorageClass)
	})
}

// InsertMedia must consume the CR through the config source, not values
// captured at startup: the DataVolume picks up a spec change on the same
// long-running manager.
func TestVirtualMachineResourceManager_InsertMediaReadsConfigFromCR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(testImageSizeBytes, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	imageURL := server.URL + "/test.iso"

	bmc := &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testBMCName},
		Spec: bmcv1.VirtualMachineBMCSpec{
			Redfish: &bmcv1.RedfishSpec{
				VirtualMedia: &bmcv1.VirtualMediaSpec{
					Storage: &bmcv1.VirtualMediaStorageSpec{
						StorageClassName: util.Ptr("cr-storage-class"),
					},
				},
			},
		},
	}
	bmcClient := newTestBMCClient(bmc)

	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).
		WithTemplate().
		WithCDRomDisk("cdrom", nil).Build()
	fakeVirtClient := kubevirtfake.NewSimpleClientset(vm)
	fakeCdiClient := cdifake.NewSimpleClientset()

	vmrm := &VirtualMachineResourceManager{
		virtClient:     fakeVirtClient,
		cdiClient:      fakeCdiClient,
		namespace:      testNamespace,
		name:           testVMName,
		vmConfigSource: NewClusterVirtualMediaConfigSource(bmcClient, testNamespace, testBMCName),
		virtualMedia:   &fakeVirtualMedia{},
	}

	require.NoError(t, vmrm.InsertMedia(context.Background(), imageURL))

	dv, err := fakeCdiClient.CdiV1beta1().DataVolumes(testNamespace).Get(context.Background(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, dv.Spec.Storage.StorageClassName)
	require.Equal(t, "cr-storage-class", *dv.Spec.Storage.StorageClassName)
}
