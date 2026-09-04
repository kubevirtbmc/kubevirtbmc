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
	s := NewSimulator("127.0.0.1", 623, nil, "admin", "secret", "default/testvm", "1.0")

	b := s.buildBMC()

	user, err := b.Users.GetByName("admin")
	assert.NoError(t, err)
	assert.True(t, user.Enabled, "registered user must be enabled")
	assert.Equal(t, bmc.PrivilegeLevelAdministrator, user.ChannelAccess[lanChannel].MaxPrivilege)
	assert.True(t, user.ChannelAccess[lanChannel].Enabled)
	assert.True(t, user.VerifyPassword([]byte("secret")))
}

func TestBuildBMCNoUserWhenUsernameEmpty(t *testing.T) {
	s := NewSimulator("127.0.0.1", 623, nil, "", "", "default/testvm", "")

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
	rm.EXPECT().GetPowerStatus(gomock.Any()).Return(true, nil)
	s := NewSimulator("127.0.0.1", 623, rm, "admin", "secret", "default/testvm", "1.0")

	b := s.buildBMC()
	ch := b.HAL().Chassis()
	assert.NotNil(t, ch, "HAL must expose Chassis for typed chassis handlers")

	on, err := ch.PowerState(context.Background())
	assert.NoError(t, err)
	assert.True(t, on)
}

func TestBuildBMCSeedsFRU(t *testing.T) {
	s := NewSimulator("127.0.0.1", 623, nil, "admin", "secret", "ns/my-vm", "2.0")
	b := s.buildBMC()

	store := b.HAL().Storage()
	assert.NotNil(t, store)

	raw, err := store.FRU().Read(context.Background(), 0)
	assert.NoError(t, err)

	fru, err := types.ParseFRU(raw)
	assert.NoError(t, err)
	assert.NotNil(t, fru.ProductInfoArea)
	p := fru.ProductInfoArea
	assert.Equal(t, "KubeVirt", types.FRUFieldString(p.ManufacturerTypeLength, p.Manufacturer))
	assert.Equal(t, "KubeVirtBMC", types.FRUFieldString(p.NameTypeLength, p.Name))
	assert.Equal(t, "2.0", types.FRUFieldString(p.VersionTypeLength, p.Version))
	assert.Equal(t, "ns/my-vm", types.FRUFieldString(p.SerialNumberTypeLength, p.SerialNumber))

	ids, err := store.SDR().RecordIDs(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, []uint16{1}, ids)
}

func TestFRUSerial(t *testing.T) {
	assert.Equal(t, "default/vm1", FRUSerial("default", "vm1"))
	// Overlong "namespace/name" is truncated at 63 bytes, keeping the
	// namespace prefix rather than falling back to the bare name.
	assert.Equal(t, strings.Repeat("a", 40)+"/"+strings.Repeat("b", 22), FRUSerial(strings.Repeat("a", 40), strings.Repeat("b", 40)))
}

// TestRunDoesNotBlockCaller is a regression test: Simulator.Run must return
// after binding so the caller (VirtBMC.Run) can start sibling services such
// as Redfish. The blocking Serve loop runs in a background goroutine, and
// Stop must wait for that goroutine to exit.
func TestRunDoesNotBlockCaller(t *testing.T) {
	// Bind to an ephemeral port on loopback so concurrent test runs don't clash.
	s := NewSimulator("127.0.0.1", 0, nil, "admin", "secret", "default/testvm", "1.0")

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
