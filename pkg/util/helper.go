package util

import (
	"crypto/tls"
	"crypto/x509"
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

// WithImportMargin pads size by marginPercent; marginPercent <= 0 is a no-op.
func WithImportMargin(size int64, marginPercent int) int64 {
	if marginPercent <= 0 {
		return size
	}
	return size + size*int64(marginPercent)/100
}

// CABundleConfigMapKey is the ConfigMap data key CDI expects a CA bundle under when referenced via CertConfigMap.
const CABundleConfigMapKey = "ca.pem"

func Ptr[T any](value T) *T {
	return &value
}

func GetRemoteFileSize(url string, insecureSkipVerify bool, caBundle []byte) (int64, error) {
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

	if insecureSkipVerify || len(caBundle) > 0 {
		tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify} //nolint:gosec // opt-in, user-controlled

		if len(caBundle) > 0 {
			pool, err := x509.SystemCertPool()
			if err != nil {
				return 0, fmt.Errorf("load system CA pool: %w", err)
			}
			if !pool.AppendCertsFromPEM(caBundle) {
				return 0, fmt.Errorf("invalid CA bundle: no certificates found")
			}
			tlsConfig.RootCAs = pool
		}

		client.Transport = &http.Transport{TLSClientConfig: tlsConfig}
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

// DataVolumeOptions holds the inputs for ConstructDataVolume. An empty StorageClassName falls back to the
// cluster default, VolumeMode falls back to CDI's own default (Filesystem) when nil, and CertConfigMap is a
// ConfigMap name in the DataVolume's namespace.
type DataVolumeOptions struct {
	Namespace          string
	Name               string
	URL                string
	Size               int64
	StorageClassName   string
	VolumeMode         *corev1.PersistentVolumeMode
	InsecureSkipVerify bool
	CertConfigMap      string
}

// ConstructDataVolume builds the DataVolume backing an inserted virtual media image.
func ConstructDataVolume(params DataVolumeOptions) *cdiv1.DataVolume {
	storage := &cdiv1.StorageSpec{
		VolumeMode: params.VolumeMode,
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: *resource.NewQuantity(params.Size, resource.BinarySI),
			},
		},
	}

	if params.VolumeMode != nil {
		storage.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}

	if params.StorageClassName != "" {
		storage.StorageClassName = &params.StorageClassName
	}

	httpSource := &cdiv1.DataVolumeSourceHTTP{
		URL: params.URL,
	}

	if params.InsecureSkipVerify {
		httpSource.InsecureSkipVerify = &params.InsecureSkipVerify
	}

	if params.CertConfigMap != "" {
		httpSource.CertConfigMap = params.CertConfigMap
	}

	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: params.Namespace,
			Name:      params.Name,
			Annotations: map[string]string{
				AnnStorageBindImmediateRequested: "",
			},
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				HTTP: httpSource,
			},
			Storage: storage,
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
