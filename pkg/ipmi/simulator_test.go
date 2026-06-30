package ipmi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/bougou/go-ipmi/pkg/bmc"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func TestBuildBMCRegistersUser(t *testing.T) {
	s := NewSimulator("127.0.0.1", 623, nil, "admin", "secret")

	b := s.buildBMC()

	user, err := b.Users.GetByName("admin")
	assert.NoError(t, err)
	assert.True(t, user.Enabled, "registered user must be enabled")
	assert.Equal(t, bmc.PrivilegeLevelAdministrator, user.ChannelAccess[lanChannel].MaxPrivilege)
	assert.True(t, user.ChannelAccess[lanChannel].Enabled)
	assert.True(t, user.VerifyPassword([]byte("secret")))
}

func TestBuildBMCNoUserWhenUsernameEmpty(t *testing.T) {
	s := NewSimulator("127.0.0.1", 623, nil, "", "")

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
	rm.EXPECT().GetSystemUUID().Return("00000000-0000-0000-0000-000000000000", nil)
	rm.EXPECT().GetPowerStatus().Return(true, nil)
	s := NewSimulator("127.0.0.1", 623, rm, "admin", "secret")

	b := s.buildBMC()
	ch := b.HAL().Chassis()
	assert.NotNil(t, ch, "HAL must expose Chassis for typed chassis handlers")

	on, err := ch.PowerState(context.Background())
	assert.NoError(t, err)
	assert.True(t, on)
}

// TestRunDoesNotBlockCaller is a regression test: Simulator.Run must return
// after binding so the caller (VirtBMC.Run) can start sibling services such
// as Redfish. The blocking Serve loop runs in a background goroutine, and
// Stop must wait for that goroutine to exit.
func TestRunDoesNotBlockCaller(t *testing.T) {
	// Bind to an ephemeral port on loopback so concurrent test runs don't clash.
	s := NewSimulator("127.0.0.1", 0, nil, "admin", "secret")

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
