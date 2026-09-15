package ipmi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/bougou/go-ipmi/pkg/bmc"
	"github.com/bougou/go-ipmi/pkg/types"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func TestBuildBMCRegistersUser(t *testing.T) {
	s := NewSimulator("127.0.0.1", 623, nil, "admin", "secret", "default/testvm")

	b := s.buildBMC()

	user, err := b.Users.GetByName("admin")
	assert.NoError(t, err)
	assert.True(t, user.Enabled, "registered user must be enabled")
	assert.Equal(t, bmc.PrivilegeLevelAdministrator, user.ChannelAccess[lanChannel].MaxPrivilege)
	assert.True(t, user.ChannelAccess[lanChannel].Enabled)
	assert.True(t, user.VerifyPassword([]byte("secret")))
}

func TestBuildBMCNoUserWhenUsernameEmpty(t *testing.T) {
	s := NewSimulator("127.0.0.1", 623, nil, "", "", "default/testvm")

	b := s.buildBMC()

	// Only the anonymous null user (ID 1) should exist.
	assert.Equal(t, 1, b.Users.Count())
	_, err := b.Users.GetByName("admin")
	assert.ErrorIs(t, err, bmc.ErrUserNotFound)
}

// TestBuildBMCHALExposesChassis ensures buildBMC wires the vmChassis adapter
// into the BMC HAL so go-ipmi's typed chassis handlers can dispatch through it.
// The PowerState call here only verifies the wiring (HAL.Chassis() returns a
// working adapter that forwards to the ResourceManager); the vmChassis business
// logic itself is exercised in handler_test.go.
func TestBuildBMCHALExposesChassis(t *testing.T) {
	rm := resourcemanager.NewMockResourceManager(gomock.NewController(t))
	rm.EXPECT().GetSystemUUID(gomock.Any()).Return("00000000-0000-0000-0000-000000000000", nil)
	rm.EXPECT().GetSystemSerial(gomock.Any()).Return("default/testvm", nil)
	rm.EXPECT().GetPowerStatus(gomock.Any()).Return(true, nil)
	s := NewSimulator("127.0.0.1", 623, rm, "admin", "secret", "default/testvm")

	b := s.buildBMC()
	ch := b.HAL().Chassis()
	assert.NotNil(t, ch, "HAL must expose Chassis for typed chassis handlers")

	on, err := ch.PowerState(context.Background())
	assert.NoError(t, err)
	assert.True(t, on)
}

func TestBuildBMCSeedsFRU(t *testing.T) {
	rm := resourcemanager.NewMockResourceManager(gomock.NewController(t))
	rm.EXPECT().GetSystemUUID(gomock.Any()).Return("5d307ca9-b3ef-428c-8861-06e72d69f223", nil)
	rm.EXPECT().GetSystemSerial(gomock.Any()).Return("e4686d2c-6e8d-4335-b8fd-81bee22f4815", nil)
	s := NewSimulator("127.0.0.1", 623, rm, "admin", "secret", "ns/my-vm")
	b := s.buildBMC()

	store := b.HAL().Storage()
	assert.NotNil(t, store)

	raw, err := store.FRU().Read(context.Background(), 0)
	assert.NoError(t, err)

	fru, err := types.ParseFRU(raw)
	assert.NoError(t, err)
	assert.NotNil(t, fru.ProductInfoArea)
	p := fru.ProductInfoArea
	assert.Equal(t, "KubeVirt", types.FRUFieldString(p.ManufacturerTypeLength, p.Manufacturer), "Product Manufacturer")
	assert.Equal(t, "ns/my-vm", types.FRUFieldString(p.NameTypeLength, p.Name), "Product Name is the managed VM")
	assert.Equal(t, "e4686d2c-6e8d-4335-b8fd-81bee22f4815", types.FRUFieldString(p.SerialNumberTypeLength, p.SerialNumber), "Product Serial is the VM's SMBIOS serial")
	// Product Version is deliberately left empty: the Product Info Area
	// describes the managed system, and a VM has no product version.
	assert.Equal(t, "", types.FRUFieldString(p.VersionTypeLength, p.Version), "Product Version stays empty")

	ids, err := store.SDR().RecordIDs(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, []uint16{1}, ids)
}

// TestSeedStorageTruncatesFRUFields covers the FRU type/length byte's 6-bit
// length: values over 63 bytes must be clipped, because an overflowing length
// collides with the type code and corrupts the rest of the area.
func TestSeedStorageTruncatesFRUFields(t *testing.T) {
	rm := resourcemanager.NewMockResourceManager(gomock.NewController(t))
	rm.EXPECT().GetSystemUUID(gomock.Any()).Return("5d307ca9-b3ef-428c-8861-06e72d69f223", nil)
	rm.EXPECT().GetSystemSerial(gomock.Any()).Return(strings.Repeat("b", 70), nil)
	s := NewSimulator("127.0.0.1", 623, rm, "admin", "secret", strings.Repeat("a", 70))
	b := s.buildBMC()

	raw, err := b.HAL().Storage().FRU().Read(context.Background(), 0)
	assert.NoError(t, err)
	fru, err := types.ParseFRU(raw)
	assert.NoError(t, err)
	p := fru.ProductInfoArea
	assert.Equal(t, strings.Repeat("a", 63), types.FRUFieldString(p.NameTypeLength, p.Name))
	assert.Equal(t, strings.Repeat("b", 63), types.FRUFieldString(p.SerialNumberTypeLength, p.SerialNumber))
}

// TestRunDoesNotBlockCaller is a regression test: Simulator.Run must return
// after binding so the caller (VirtBMC.Run) can start sibling services such
// as Redfish. The blocking Serve loop runs in a background goroutine, and
// Stop must wait for that goroutine to exit.
func TestRunDoesNotBlockCaller(t *testing.T) {
	// Bind to an ephemeral port on loopback so concurrent test runs don't clash.
	s := NewSimulator("127.0.0.1", 0, nil, "admin", "secret", "default/testvm")

	done := make(chan error, 1)
	go func() { done <- s.Run() }()

	select {
	case err := <-done:
		assert.NoError(t, err, "Run must return after bind, not block on Serve")
	case <-time.After(3 * time.Second):
		t.Fatal("Simulator.Run blocked the caller; Serve must run in a goroutine")
	}

	stopDone := make(chan struct{}, 1)
	go func() { s.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Simulator.Stop did not return; serve goroutine did not exit")
	}
}
