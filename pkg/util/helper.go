package util

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// ZeroSystemUUID is the RFC 4122 null UUID, reported when the VM's firmware.uuid
// is unusable: IPMI's Get System GUID is always 16 bytes (§22.14) and Redfish
// wants a Resource_UUID-shaped string, so neither can express "absent".
const ZeroSystemUUID = "00000000-0000-0000-0000-000000000000"

// kubevirtFirmwareUUIDns is KubeVirt's unexported magicUUID, the namespace of
// CalculateLegacyUUID (pkg/virt-controller/watch/vm/firmware.go): frozen upstream
// — unchanged from v1.0.0 to v1.9.0 — so it is copied rather than imported.
const kubevirtFirmwareUUIDns = "6a1a24a1-4061-4607-8bf4-a3963d0c5895"

// SystemName returns the system identity as "<namespace>/<name>", reported as
// Redfish ComputerSystem.Name and the FRU Product Name.
func SystemName(namespace, name string) string {
	return types.NamespacedName{Namespace: namespace, Name: name}.String()
}

// LegacyFirmwareUUID mirrors KubeVirt's CalculateLegacyUUID: the UUIDv5
// (RFC 4122 §4.3) of the VM name that KubeVirt writes into firmware.uuid when it
// is empty, and that the guest sees.
func LegacyFirmwareUUID(name string) string {
	return uuid.NewSHA1(uuid.MustParse(kubevirtFirmwareUUIDns), []byte(name)).String()
}

// SystemUUID resolves the SMBIOS system UUID the BMC reports from the VM's
// firmware.uuid, which KubeVirt never validates: a valid value is normalized, an
// empty one falls back to KubeVirt's legacy UUID, an unusable one to null.
func SystemUUID(firmwareUUID, vmName string) string {
	if firmwareUUID == "" {
		return LegacyFirmwareUUID(vmName)
	}
	parsed, err := uuid.Parse(firmwareUUID)
	if err != nil {
		return ZeroSystemUUID
	}
	return parsed.String()
}

// SystemSerial resolves the system serial the BMC reports (FRU Product Serial
// Number and Redfish ComputerSystem.SerialNumber) from the VM's firmware.serial,
// falling back to the VM's UID when KubeVirt's create-time webhook left it empty.
func SystemSerial(firmwareSerial, vmUID string) string {
	if firmwareSerial == "" {
		return vmUID
	}
	return firmwareSerial
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
