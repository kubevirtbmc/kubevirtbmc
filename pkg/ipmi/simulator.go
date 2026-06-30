package ipmi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/bougou/go-ipmi/pkg/bmc"
	"github.com/bougou/go-ipmi/pkg/handlers"
	"github.com/bougou/go-ipmi/pkg/server"
	udptransport "github.com/bougou/go-ipmi/pkg/transport/udp"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// lanChannel is the IPMI LAN channel number used by the simulator (channel 1,
// the default LAN channel in go-ipmi's ChannelStore).
const lanChannel uint8 = 1

// Simulator is an IPMI BMC simulator backed by a KubeVirt VirtualMachine.
//
// It wires github.com/bougou/go-ipmi's server stack (RMCP+ / IPMI v2.0 LANPLUS,
// plus minimal pre-session v1.0 LAN handling) to a ResourceManager. Chassis
// commands are routed to the ResourceManager through the typed hal.ChassisHAL
// implementation in handler.go (vmChassis); session establishment, RAKP,
// encryption and framing are handled by go-ipmi.
//
// This file contains only the simulator lifecycle (bind/serve/stop) and BMC
// state construction. All KubeVirt-specific chassis business logic lives in
// handler.go.
type Simulator struct {
	ip       string
	port     int
	rm       resourcemanager.ResourceManager
	username string
	password string

	srv    *server.Server
	conn   *udptransport.Conn
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSimulator creates a new IPMI simulator.
//
// The simulator does not bind the UDP socket until Run is called.
func NewSimulator(ip string, port int, resourceManager resourcemanager.ResourceManager, username, password string) *Simulator {
	return &Simulator{
		ip:       ip,
		port:     port,
		rm:       resourceManager,
		username: username,
		password: password,
	}
}

// Run binds the UDP socket, builds the BMC state, and starts serving IPMI
// requests in a background goroutine. It returns as soon as the socket is
// bound so the caller (VirtBMC.Run) can proceed to start other services
// (e.g. Redfish). A bind failure is returned synchronously. Serve-time
// errors are logged, not returned. Use Stop to wait for the goroutine to exit.
func (s *Simulator) Run() error {
	listenIP := s.ip
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	addr := net.JoinHostPort(listenIP, fmt.Sprintf("%d", s.port))
	conn, err := udptransport.Listen(addr)
	if err != nil {
		return fmt.Errorf("listen udp %q: %w", addr, err)
	}
	s.conn = conn

	b := s.buildBMC()

	reg := handlers.NewRegistry()
	handlers.RegisterAppHandlers(reg)
	handlers.RegisterSessionHandlers(reg)
	// RegisterChassisHandlers installs go-ipmi's typed codec handlers
	// (Chassis Control, Set/Get System Boot Options, Get Chassis Status, ...).
	// They dispatch through hal.ChassisHAL, which we back with vmChassis below
	// so each spec action maps to the corresponding KubeVirt ResourceManager API.
	handlers.RegisterChassisHandlers(reg)

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.srv = server.NewServer(b, conn, server.WithHandlerRegistry(reg))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.srv.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logrus.WithError(err).Error("IPMI server exited with error")
		}
	}()

	return nil
}

// Stop gracefully shuts down the simulator: cancels the serve context, closes
// the UDP socket, and waits for the background serve goroutine to exit.
func (s *Simulator) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.srv != nil {
		_ = s.srv.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.wg.Wait()
	logrus.Info("IPMI simulator gracefully stopped")
}

// resolveGUID returns the VM's UUID as a 16-byte GUID, falling back to an
// all-zero GUID when the ResourceManager is nil (e.g. in unit tests). Using
// the real VM UUID binds the RAKP key exchange to the specific VM — each VM
// gets different HMAC inputs — and makes the GUID returned by Get Device GUID
// (App 0x08) meaningful rather than returning a zero identifier.
func (s *Simulator) resolveGUID() [16]byte {
	if s.rm == nil {
		return [16]byte{}
	}
	uidStr, err := s.rm.GetSystemUUID()
	if err != nil {
		logrus.WithError(err).Warn("failed to get system UUID, falling back to zero GUID")
		return [16]byte{}
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		logrus.WithError(err).Warnf("failed to parse system UUID %q, falling back to zero GUID", uidStr)
		return [16]byte{}
	}
	return uid
}

// buildBMC constructs the in-memory BMC state: device identity, GUID, the
// authenticated user account, and a HAL whose Chassis sub-interface is backed
// by vmChassis (the KubeVirt ResourceManager adapter). go-ipmi's typed chassis
// handlers dispatch through that HAL.
func (s *Simulator) buildBMC() *bmc.BMC {
	info := bmc.DeviceInfo{
		DeviceID:                0x20,
		DeviceRevision:          0x01,
		FirmwareMajor:           0x01,
		FirmwareMinor:           0x00,
		IPMIVersion:             0x20, // IPMI 2.0
		ManufacturerID:          0x000000,
		ProductID:               0x0000,
		AdditionalDeviceSupport: 0x00,
	}

	guid := s.resolveGUID()

	b := bmc.New(info, guid, noopHAL{chassis: vmChassis{rm: s.rm}}, bmc.WithKG(nil))

	// Register the configured BMC user so RAKP username/password auth succeeds.
	if s.username != "" {
		user, err := b.Users.Add(2, s.username)
		if err != nil {
			logrus.WithError(err).Warn("failed to register IPMI user")
		} else {
			user.SetPassword([]byte(s.password))
			user.Enabled = true
			user.ChannelAccess = map[uint8]bmc.UserChannelAccess{
				lanChannel: {
					MaxPrivilege: bmc.PrivilegeLevelAdministrator,
					Enabled:      true,
				},
			}
		}
	}

	return b
}
