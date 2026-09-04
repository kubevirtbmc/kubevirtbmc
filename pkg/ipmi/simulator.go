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
	"github.com/bougou/go-ipmi/pkg/types"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

// lanChannel is the IPMI LAN channel number used by the simulator (channel 1,
// the default LAN channel in go-ipmi's ChannelStore).
const lanChannel uint8 = 1

// Simulator is an IPMI BMC simulator backed by a KubeVirt VirtualMachine.
//
// It wires github.com/bougou/go-ipmi's server stack (RMCP+ / IPMI v2.0 LANPLUS,
// plus minimal pre-session v1.0 LAN handling) to a ResourceManager. Chassis
// commands are routed to the ResourceManager through the typed hal.ChassisHAL
// implementation in handler.go (vmChassis); FRU/SDR commands go through
// go-ipmi's RegisterStorageHandlers against an in-memory store seeded at
// buildBMC time. Session establishment, RAKP, encryption and framing are
// handled by go-ipmi.
type Simulator struct {
	ip             string
	port           int
	rm             resourcemanager.ResourceManager
	username       string
	password       string
	serial         string // FRU Product Serial (namespace/name)
	productVersion string // FRU Product Version (git commit SHA)

	srv    *server.Server
	conn   *udptransport.Conn
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSimulator creates a new IPMI simulator.
//
// serial is the FRU Product Serial (typically "<namespace>/<vm-name>").
// productVersion is the FRU Product Version; pass the git commit SHA
// (main.GitCommit) — always within the FRU 63-byte field limit, unlike
// branch-derived version strings.
// The simulator does not bind the UDP socket until Run is called.
func NewSimulator(ip string, port int, resourceManager resourcemanager.ResourceManager, username, password, serial, productVersion string) *Simulator {
	return &Simulator{
		ip:             ip,
		port:           port,
		rm:             resourceManager,
		username:       username,
		password:       password,
		serial:         serial,
		productVersion: productVersion,
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
	// Use before Register* so middleware wraps every command handler.
	reg.Use(accessLogMiddleware)
	handlers.RegisterAppHandlers(reg)
	handlers.RegisterSessionHandlers(reg)
	// RegisterChassisHandlers installs go-ipmi's typed codec handlers
	// (Chassis Control, Set/Get System Boot Options, Get Chassis Status, ...).
	// They dispatch through hal.ChassisHAL, which we back with vmChassis below
	// so each spec action maps to the corresponding KubeVirt ResourceManager API.
	handlers.RegisterChassisHandlers(reg)
	handlers.RegisterStorageHandlers(reg)

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
	uidStr, err := s.rm.GetSystemUUID(context.Background())
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
// by vmChassis and whose Storage is seeded with a Product Info FRU plus an MC
// Locator SDR so ipmitool fru list works via go-ipmi's stock Storage handlers.
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

	store := newMemoryStorage()
	s.seedStorage(store)

	guid := s.resolveGUID()

	chassis := loggingChassis{ChassisHAL: vmChassis{rm: s.rm}}
	b := bmc.New(info, guid, noopHAL{
		chassis: chassis,
		storage: store,
	}, bmc.WithKG(nil))

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

// seedStorage writes the Builtin FRU (device 0) and a Type 12h MC Locator SDR
// so clients can discover and read product identity via Storage NetFn.
func (s *Simulator) seedStorage(store *memoryStorage) {
	serial := s.serial
	if serial == "" {
		serial = "unknown"
	}
	// FRU type/length length field is 6 bits (max 63); go-ipmi's PackFRU
	// silently mangles longer fields instead of returning an error.
	if len(serial) > 63 {
		serial = serial[:63]
	}
	version := s.productVersion
	if len(version) > 63 {
		version = version[:63]
	}

	fruData, err := types.PackFRU(types.FRUPackConfig{
		Product: &types.FRUPackProduct{
			Manufacturer: "KubeVirt",
			Name:         "KubeVirtBMC",
			Version:      version,
			Serial:       serial,
		},
	})
	if err != nil {
		logrus.WithError(err).Warn("failed to pack FRU data")
		return
	}
	ctx := context.Background()
	if err := store.FRU().Write(ctx, 0, fruData); err != nil {
		logrus.WithError(err).Warn("failed to seed FRU device 0")
	}
	if err := store.SDR().Write(ctx, 1, types.PackMCLocator(types.MCLocatorPackOpts{
		RecordID: 1,
	})); err != nil {
		logrus.WithError(err).Warn("failed to seed MC Locator SDR")
	}
}

// FRUSerial builds the FRU Product Serial from VM identity.
// Same base form as [util.SystemSerial], but truncated for the FRU type/length
// 6-bit length field (max 63). Truncation keeps the namespace prefix, so VMs
// sharing a name across namespaces stay distinguishable; falling back to the
// bare name would not. Redfish must not use this helper.
func FRUSerial(namespace, name string) string {
	serial := util.SystemSerial(namespace, name)
	if len(serial) > 63 {
		serial = serial[:63]
	}
	return serial
}
