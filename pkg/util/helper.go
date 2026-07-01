package util

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

// AnnStorageBindImmediateRequested is the DataVolume annotation that requests
// immediate binding of the underlying PVC, bypassing WaitForFirstConsumer.
const AnnStorageBindImmediateRequested = "cdi.kubevirt.io/storage.bind.immediate.requested"

func Ptr[T any](value T) *T {
	return &value
}

func GetRemoteFileSize(url string) (int64, error) {
	parsedURL, err := neturl.Parse(url)
	if err != nil {
		return 0, fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return 0, fmt.Errorf("invalid scheme: only http/https allowed")
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Head(parsedURL.String())
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("bad status: %s", resp.Status)
	}

	size := resp.ContentLength
	if size < 0 {
		return 0, fmt.Errorf("content-length not available")
	}

	return size, nil
}

func ConstructDataVolume(namespace, name, url string, size int64) *cdiv1.DataVolume {
	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Annotations: map[string]string{
				AnnStorageBindImmediateRequested: "",
			},
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				HTTP: &cdiv1.DataVolumeSourceHTTP{
					URL: url,
				},
			},
			Storage: &cdiv1.StorageSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: *resource.NewQuantity(size, resource.BinarySI),
					},
				},
			},
		},
	}
}

func GetCdromDisk(disks []kubevirtv1.Disk) (*kubevirtv1.Disk, error) {
	for i := range disks {
		if disks[i].CDRom != nil {
			return &disks[i], nil
		}
	}

	return nil, fmt.Errorf("no cdrom disks can be found")
}
