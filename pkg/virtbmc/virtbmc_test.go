package virtbmc

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"kubevirt.io/kubevirtbmc/pkg/util"
)

func TestParseVolumeMode(t *testing.T) {
	testCases := []struct {
		name      string
		input     string
		expect    *corev1.PersistentVolumeMode
		shouldErr bool
	}{
		{name: "empty falls back to CDI default"},
		{name: "block", input: "block", expect: util.Ptr(corev1.PersistentVolumeBlock)},
		{name: "filesystem", input: "filesystem", expect: util.Ptr(corev1.PersistentVolumeFilesystem)},
		{name: "case-insensitive", input: "Block", expect: util.Ptr(corev1.PersistentVolumeBlock)},
		{name: "invalid value rejected", input: "bogus", shouldErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVolumeMode(tc.input)
			if tc.shouldErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, got)
		})
	}
}
