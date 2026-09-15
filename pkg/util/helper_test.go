package util

import (
	"crypto/sha1"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestWithImportMargin(t *testing.T) {
	tests := []struct {
		name          string
		size          int64
		marginPercent int
		want          int64
	}{
		{name: "zero margin is a no-op", size: 333688832, marginPercent: 0, want: 333688832},
		{name: "negative margin is a no-op", size: 333688832, marginPercent: -10, want: 333688832},
		{name: "typical boot ISO size", size: 333688832, marginPercent: 30, want: 433795481},
		{name: "1 GiB", size: 1 << 30, marginPercent: 30, want: 1395864371},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithImportMargin(tt.size, tt.marginPercent); got != tt.want {
				t.Errorf("WithImportMargin(%d, %d) = %d, want %d", tt.size, tt.marginPercent, got, tt.want)
			}
		})
	}
}

func TestSystemName(t *testing.T) {
	assert.Equal(t, "default/vm1", SystemName("default", "vm1"))
}

func TestSystemSerial(t *testing.T) {
	const vmuid = "0f3a5b7c-2d4e-4f6a-8b9c-0d1e2f3a4b5c"

	assert.Equal(t, "e4686d2c-6e8d-4335-b8fd-81bee22f4815",
		SystemSerial("e4686d2c-6e8d-4335-b8fd-81bee22f4815", vmuid))
	assert.Equal(t, vmuid, SystemSerial("", vmuid), "an empty firmware.serial falls back to the VM UID")
}

func TestSystemUUID(t *testing.T) {
	const name = "vm1"

	tests := []struct {
		name         string
		firmwareUUID string
		want         string
	}{
		{"valid UUID is reported", "5d307ca9-b3ef-428c-8861-06e72d69f223", "5d307ca9-b3ef-428c-8861-06e72d69f223"},
		{"valid UUID is normalized to the RFC 4122 string form", "5D307CA9B3EF428C886106E72D69F223", "5d307ca9-b3ef-428c-8861-06e72d69f223"},
		{"empty falls back to KubeVirt's legacy UUID", "", LegacyFirmwareUUID(name)},
		{"unparseable falls back to the null UUID", "not-a-uuid", ZeroSystemUUID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SystemUUID(tt.firmwareUUID, name))
		})
	}
}

// TestLegacyFirmwareUUIDMatchesKubeVirt recomputes KubeVirt's CalculateLegacyUUID
// by hand: SHA-1 over the firmwareUUIDns namespace bytes followed by the VM name,
// with the version and variant bits set per RFC 4122 §4.3 (UUIDv5).
func TestLegacyFirmwareUUIDMatchesKubeVirt(t *testing.T) {
	const name = "vm1"
	ns := uuid.MustParse(kubevirtFirmwareUUIDns)

	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(name))

	var want uuid.UUID
	copy(want[:], h.Sum(nil)[:16])
	want[6] = (want[6] & 0x0f) | 0x50 // version 5
	want[8] = (want[8] & 0x3f) | 0x80 // RFC 4122 variant

	assert.Equal(t, want.String(), LegacyFirmwareUUID(name))
	assert.NotEqual(t, LegacyFirmwareUUID(name), LegacyFirmwareUUID("vm2"), "the UUID is derived per VM name")
}
