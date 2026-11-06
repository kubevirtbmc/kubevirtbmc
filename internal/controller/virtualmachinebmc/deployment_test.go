package virtualmachinebmc

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// Virtual media settings must never leak into the pod template: the agent
// reads them from the CR at InsertMedia time, so a spec/annotation edit that
// re-renders the template (and rolls the pod) is a regression.
func TestCreateVirtBMCDeploymentVirtualMediaDoesNotDriftPodTemplate(t *testing.T) {
	r := &VirtualMachineBMCReconciler{}
	base := &bmcv1.VirtualMachineBMC{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bmc", Namespace: "default"},
		Spec: bmcv1.VirtualMachineBMCSpec{
			VirtualMachineRef: &corev1.LocalObjectReference{Name: "testvm"},
		},
	}
	withMedia := base.DeepCopy()
	withMedia.Annotations = map[string]string{bmcv1.AnnotationDataVolumeSizeMargin: "30"}
	withMedia.Spec.Redfish = &bmcv1.RedfishSpec{
		VirtualMedia: &bmcv1.VirtualMediaSpec{
			Storage: &bmcv1.VirtualMediaStorageSpec{
				StorageClassName: ptr.To("fast-sc"),
				VolumeMode:       ptr.To(corev1.PersistentVolumeBlock),
			},
			TLS: &bmcv1.VirtualMediaTLSSpec{
				InsecureSkipVerify:   ptr.To(true),
				CABundleConfigMapRef: &corev1.LocalObjectReference{Name: "custom-ca"},
			},
		},
	}

	templateA := r.createVirtBMCDeployment(base, "").Spec.Template
	templateB := r.createVirtBMCDeployment(withMedia, "").Spec.Template
	if !reflect.DeepEqual(templateA, templateB) {
		t.Errorf("virtual media spec changed the pod template:\n%+v\nvs\n%+v", templateA, templateB)
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
