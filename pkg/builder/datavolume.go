package builder

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

type DataVolumeBuilder struct {
	dv *cdiv1.DataVolume
}

func NewDataVolumeBuilder(namespace, name string) *DataVolumeBuilder {
	return &DataVolumeBuilder{
		dv: &cdiv1.DataVolume{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
		},
	}
}

func (b *DataVolumeBuilder) WithAnnotation(key, value string) *DataVolumeBuilder {
	if b.dv.Annotations == nil {
		b.dv.Annotations = map[string]string{}
	}
	b.dv.Annotations[key] = value
	return b
}

func (b *DataVolumeBuilder) Build() *cdiv1.DataVolume {
	return b.dv
}

func (b *DataVolumeBuilder) WithHTTPSource(url string) *DataVolumeBuilder {
	b.dv.Spec.Source = &cdiv1.DataVolumeSource{
		HTTP: &cdiv1.DataVolumeSourceHTTP{
			URL: url,
		},
	}
	return b
}

func (b *DataVolumeBuilder) WithStorage(size int64) *DataVolumeBuilder {
	b.dv.Spec.Storage = &cdiv1.StorageSpec{
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: *resource.NewQuantity(size, resource.BinarySI),
			},
		},
	}
	return b
}

func (b *DataVolumeBuilder) WithStorageClass(name string) *DataVolumeBuilder {
	if b.dv.Spec.Storage == nil {
		b.dv.Spec.Storage = &cdiv1.StorageSpec{}
	}
	b.dv.Spec.Storage.StorageClassName = &name
	return b
}
