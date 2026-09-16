package resourcemanager

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// VirtualMediaConfig carries the knobs consumed when InsertMedia builds the
// image DataVolume.
type VirtualMediaConfig struct {
	StorageClass       string
	VolumeMode         *corev1.PersistentVolumeMode
	SizeMarginPercent  int
	InsecureSkipVerify bool
	CABundleConfigMap  string
}

// VirtualMediaConfigSource resolves the virtual media config at InsertMedia
// time. It mirrors the StateStore split: managed mode reads the
// VirtualMachineBMC CR so spec changes take effect on the next insert without
// restarting the agent; standalone mode has no CR and returns the static flag
// values captured at startup.
type VirtualMediaConfigSource interface {
	Resolve(ctx context.Context) (VirtualMediaConfig, error)
}

type staticVirtualMediaConfigSource struct {
	cfg VirtualMediaConfig
}

func NewStaticVirtualMediaConfigSource(cfg VirtualMediaConfig) VirtualMediaConfigSource {
	return staticVirtualMediaConfigSource{cfg: cfg}
}

func (s staticVirtualMediaConfigSource) Resolve(context.Context) (VirtualMediaConfig, error) {
	return s.cfg, nil
}

type clusterVirtualMediaConfigSource struct {
	bmcClient client.Client
	namespace string
	bmcName   string
}

func NewClusterVirtualMediaConfigSource(bmcClient client.Client, namespace, bmcName string) VirtualMediaConfigSource {
	return &clusterVirtualMediaConfigSource{bmcClient: bmcClient, namespace: namespace, bmcName: bmcName}
}

func (s *clusterVirtualMediaConfigSource) Resolve(ctx context.Context) (VirtualMediaConfig, error) {
	bmc := &bmcv1.VirtualMachineBMC{}
	if err := s.bmcClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.bmcName}, bmc); err != nil {
		return VirtualMediaConfig{}, err
	}
	return virtualMediaConfigFromSpec(bmc), nil
}

// virtualMediaConfigFromSpec translates spec.redfish.virtualMedia plus the
// size-margin annotation. An invalid margin is treated as unset, matching the
// annotation's documented "absent/invalid defaults to 0" contract.
func virtualMediaConfigFromSpec(bmc *bmcv1.VirtualMachineBMC) VirtualMediaConfig {
	var cfg VirtualMediaConfig
	if sc := bmc.Spec.VirtualMediaStorageClassName(); sc != nil {
		cfg.StorageClass = *sc
	}
	cfg.VolumeMode = bmc.Spec.VirtualMediaVolumeMode()
	if tls := bmc.Spec.RedfishVirtualMediaTLS(); tls != nil {
		if tls.InsecureSkipVerify != nil {
			cfg.InsecureSkipVerify = *tls.InsecureSkipVerify
		}
		if ref := tls.CABundleConfigMapRef; ref != nil {
			cfg.CABundleConfigMap = ref.Name
		}
	}
	if margin, ok := bmc.Annotations[bmcv1.AnnotationDataVolumeSizeMargin]; ok {
		if percent, err := strconv.Atoi(margin); err == nil && percent > 0 {
			cfg.SizeMarginPercent = percent
		}
	}
	return cfg
}
