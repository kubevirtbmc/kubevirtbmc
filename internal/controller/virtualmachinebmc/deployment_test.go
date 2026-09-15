package virtualmachinebmc

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// The agent takes the StorageClass from its --storage-class flag; the CR field
// only reaches the agent through the rendered Deployment args.
func TestCreateVirtBMCDeploymentStorageClassArg(t *testing.T) {
	r := &VirtualMachineBMCReconciler{}
	newBMC := func(sc *string) *bmcv1.VirtualMachineBMC {
		return &bmcv1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{Name: "test-bmc", Namespace: "default"},
			Spec: bmcv1.VirtualMachineBMCSpec{
				VirtualMachineRef: &corev1.LocalObjectReference{Name: "testvm"},
				Redfish: &bmcv1.RedfishSpec{
					VirtualMedia: &bmcv1.VirtualMediaSpec{
						Storage: &bmcv1.VirtualMediaStorageSpec{
							StorageClassName: sc,
						},
					},
				},
			},
		}
	}

	args := r.createVirtBMCDeployment(newBMC(ptr.To("fast-sc")), "").Spec.Template.Spec.Containers[0].Args
	if idx := slices.Index(args, "--storage-class"); idx < 0 || args[idx+1] != "fast-sc" {
		t.Errorf("args %v should contain --storage-class fast-sc", args)
	}

	for _, sc := range []*string{nil, ptr.To("")} {
		args := r.createVirtBMCDeployment(newBMC(sc), "").Spec.Template.Spec.Containers[0].Args
		if slices.Contains(args, "--storage-class") {
			t.Errorf("args %v should omit --storage-class when unset or empty", args)
		}
	}
}

// Virtual media VolumeMode and the size-margin annotation reach the agent as
// rendered --volume-mode / --datavolume-size-margin flags, same as the
// storage class above.
func TestCreateVirtBMCDeploymentMediaArgs(t *testing.T) {
	r := &VirtualMachineBMCReconciler{}
	newBMC := func(volumeMode *corev1.PersistentVolumeMode, margin string) *bmcv1.VirtualMachineBMC {
		annotations := map[string]string{}
		if margin != "" {
			annotations[bmcv1.AnnotationDataVolumeSizeMargin] = margin
		}
		return &bmcv1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{Name: "test-bmc", Namespace: "default", Annotations: annotations},
			Spec: bmcv1.VirtualMachineBMCSpec{
				VirtualMachineRef: &corev1.LocalObjectReference{Name: "testvm"},
				Redfish: &bmcv1.RedfishSpec{
					VirtualMedia: &bmcv1.VirtualMediaSpec{
						Storage: &bmcv1.VirtualMediaStorageSpec{
							VolumeMode: volumeMode,
						},
					},
				},
			},
		}
	}

	args := func(bmc *bmcv1.VirtualMachineBMC) []string {
		return r.createVirtBMCDeployment(bmc, "").Spec.Template.Spec.Containers[0].Args
	}

	if args := args(newBMC(ptr.To(corev1.PersistentVolumeBlock), "")); slices.Index(args, "--volume-mode") < 0 || args[slices.Index(args, "--volume-mode")+1] != "block" {
		t.Errorf("args %v should contain --volume-mode block", args)
	}
	if args := args(newBMC(nil, "30")); slices.Index(args, "--datavolume-size-margin") < 0 || args[slices.Index(args, "--datavolume-size-margin")+1] != "30" {
		t.Errorf("args %v should contain --datavolume-size-margin 30", args)
	}
	for _, bmc := range []*bmcv1.VirtualMachineBMC{
		newBMC(nil, ""),
		newBMC(nil, "invalid"),
		newBMC(nil, "0"),
	} {
		if args := args(bmc); slices.Contains(args, "--volume-mode") || slices.Contains(args, "--datavolume-size-margin") {
			t.Errorf("args %v should omit media flags when unset or invalid", args)
		}
	}
}

// redfish.virtualMedia.tls reaches the agent as rendered
// --virtual-media-insecure-skip-verify / --virtual-media-ca-bundle-configmap
// flags, same as the storage settings above.
func TestCreateVirtBMCDeploymentTLSArgs(t *testing.T) {
	r := &VirtualMachineBMCReconciler{}
	newBMC := func(tls *bmcv1.VirtualMediaTLSSpec) *bmcv1.VirtualMachineBMC {
		return &bmcv1.VirtualMachineBMC{
			ObjectMeta: metav1.ObjectMeta{Name: "test-bmc", Namespace: "default"},
			Spec: bmcv1.VirtualMachineBMCSpec{
				VirtualMachineRef: &corev1.LocalObjectReference{Name: "testvm"},
				Redfish: &bmcv1.RedfishSpec{
					VirtualMedia: &bmcv1.VirtualMediaSpec{TLS: tls},
				},
			},
		}
	}

	args := func(bmc *bmcv1.VirtualMachineBMC) []string {
		return r.createVirtBMCDeployment(bmc, "").Spec.Template.Spec.Containers[0].Args
	}

	full := args(newBMC(&bmcv1.VirtualMediaTLSSpec{
		InsecureSkipVerify:   ptr.To(true),
		CABundleConfigMapRef: &corev1.LocalObjectReference{Name: "custom-ca"},
	}))
	if !slices.Contains(full, "--virtual-media-insecure-skip-verify") {
		t.Errorf("args %v should contain --virtual-media-insecure-skip-verify", full)
	}
	if idx := slices.Index(full, "--virtual-media-ca-bundle-configmap"); idx < 0 || full[idx+1] != "custom-ca" {
		t.Errorf("args %v should contain --virtual-media-ca-bundle-configmap custom-ca", full)
	}

	for _, bmc := range []*bmcv1.VirtualMachineBMC{
		newBMC(nil),
		newBMC(&bmcv1.VirtualMediaTLSSpec{}),
		newBMC(&bmcv1.VirtualMediaTLSSpec{InsecureSkipVerify: ptr.To(false)}),
	} {
		if args := args(bmc); slices.Contains(args, "--virtual-media-insecure-skip-verify") || slices.Contains(args, "--virtual-media-ca-bundle-configmap") {
			t.Errorf("args %v should omit TLS flags when unset or false", args)
		}
	}
}

func TestCreateVirtBMCDeploymentUsesSingleWriterStrategy(t *testing.T) {
	r := &VirtualMachineBMCReconciler{}
	bmc := &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bmc", Namespace: "default"},
		Spec: bmcv1.VirtualMachineBMCSpec{
			VirtualMachineRef: &corev1.LocalObjectReference{Name: "testvm"},
		},
	}

	deployment := r.createVirtBMCDeployment(bmc, "")
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("virtbmc agent must have exactly one replica")
	}
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("virtbmc agent must use Recreate strategy, got %q", deployment.Spec.Strategy.Type)
	}
}
