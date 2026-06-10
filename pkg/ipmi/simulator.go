package ipmi

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"hash/adler32"
	"net"
	"sync"

	"github.com/sirupsen/logrus"
	goipmi "github.com/vmware/goipmi"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// Command-specific completion codes for IPMI session management.
// These are in the 0x80-0xBE range per the IPMI spec (section 5.2).
const (
	// ActivateSession (22.17): auth failure / invalid AuthCode
	ccInvalidAuthCode = goipmi.CompletionCode(0x85)
	// ActivateSession (22.17): invalid session ID
	ccInvalidSessionID = goipmi.CompletionCode(0x86)
)

// v15Session holds state for an IPMI v1.5 session challenge-response handshake.
type v15Session struct {
	username  string
	challenge [16]byte
}

// Simulator provides an IPMI BMC simulator.
type Simulator struct {
	ip   string
	port int

	handler *handler
	sim     *goipmi.Simulator

	// Authentication credentials
	username string
	password string

	// v1.5 session state (temporary session ID -> session state)
	v15Sessions   map[uint32]*v15Session
	v15SessionsMu sync.Mutex
}

// NewSimulator creates a new IPMI simulator with the given credentials.
func NewSimulator(ip string, port int, resourceManager resourcemanager.ResourceManager, username, password string) *Simulator {
	return &Simulator{
		ip:   ip,
		port: port,

		handler: NewHandler(resourceManager),
		sim: goipmi.NewSimulator(net.UDPAddr{
			IP:   net.ParseIP(ip).To4(),
			Port: port,
		}),
		username:    username,
		password:    password,
		v15Sessions: map[uint32]*v15Session{},
	}
}

func (s *Simulator) initialize() {
	// Override chassis command handlers
	s.sim.SetHandler(
		goipmi.NetworkFunctionChassis,
		goipmi.CommandChassisStatus,
		s.handler.chassisStatusHandler,
	)
	s.sim.SetHandler(
		goipmi.NetworkFunctionChassis,
		goipmi.CommandChassisControl,
		s.handler.chassisControlHandler,
	)
	s.sim.SetHandler(
		goipmi.NetworkFunctionChassis,
		goipmi.CommandSetSystemBootOptions,
		s.handler.setSystemBootOptionsHandler,
	)

	// Override session management handlers with authenticated versions
	s.sim.SetHandler(
		goipmi.NetworkFunctionApp,
		goipmi.CommandGetSessionChallenge,
		s.handleSessionChallenge,
	)
	s.sim.SetHandler(
		goipmi.NetworkFunctionApp,
		goipmi.CommandActivateSession,
		s.handleActivateSession,
	)
}

// handleSessionChallenge validates the username and returns a random challenge.
func (s *Simulator) handleSessionChallenge(m *goipmi.Message) goipmi.Response {
	r := &goipmi.SessionChallengeRequest{}
	if err := m.Request(r); err != nil {
		return err
	}

	// Extract username from the 16-byte username field (trim null bytes)
	username := string(bytes.TrimRight(r.Username[:], "\x00"))

	return s.processSessionChallenge(username)
}

// processSessionChallenge is the testable core of handleSessionChallenge.
func (s *Simulator) processSessionChallenge(username string) goipmi.Response {
	if username != s.username {
		logrus.Warnf("IPMI v1.5 session challenge: invalid username %q", username)
		return &goipmi.SessionChallengeResponse{
			CompletionCode: ccInvalidAuthCode,
		}
	}

	// Generate a temporary session ID from the username hash
	hash := adler32.New()
	hash.Write([]byte(username))
	id := hash.Sum32()

	// Generate a random challenge
	var challenge [16]byte
	_, _ = rand.Read(challenge[:])

	s.v15SessionsMu.Lock()
	s.v15Sessions[id] = &v15Session{username: username, challenge: challenge}
	s.v15SessionsMu.Unlock()

	logrus.Debugf("IPMI v1.5 session challenge: user=%q sessionID=0x%08x", username, id)

	return &goipmi.SessionChallengeResponse{
		CompletionCode:     goipmi.CommandCompleted,
		TemporarySessionID: id,
		Challenge:          challenge,
	}
}

// handleActivateSession validates the password and activates a v1.5 session.
func (s *Simulator) handleActivateSession(m *goipmi.Message) goipmi.Response {
	r := &goipmi.ActivateSessionRequest{}
	if err := m.Request(r); err != nil {
		return err
	}

	// Reconstruct the IPMI payload bytes for multi-session auth code validation.
	// Per IPMI spec section 22.17.1:
	//   Multi-session AuthCode = MD5(password[16] | sessionID[4] | ipmi_payload | seq[4] | password[16])
	//   where ipmi_payload = RsAddr | NetFnRsLUN | Checksum1 | RqAddr | RqSeq | Command | Data | Checksum2
	// The trailing checksum (Checksum2) is included because ipmitool (and the goipmi client)
	// compute the AuthCode over the full serialized IPMI message starting at RsAddr.
	ipmiPayload := make([]byte, 7+len(m.Data))
	ipmiPayload[0] = m.RsAddr
	ipmiPayload[1] = m.NetFnRsLUN
	ipmiPayload[2] = m.Checksum
	ipmiPayload[3] = m.RqAddr
	ipmiPayload[4] = m.RqSeq
	ipmiPayload[5] = uint8(m.Command)
	copy(ipmiPayload[6:], m.Data)
	// Recompute Checksum2 the same way goipmi does.
	ipmiPayload[6+len(m.Data)] = payloadChecksum(m.RqAddr, m.RqSeq, uint8(m.Command), m.Data)

	return s.processActivateSession(m.SessionID, r.AuthType, m.Sequence, m.AuthCode, ipmiPayload)
}

// processActivateSession is the testable core of handleActivateSession.
func (s *Simulator) processActivateSession(sessionID uint32, authType uint8, sequence uint32, sessionAuthCode [16]byte, ipmiPayload []byte) goipmi.Response {
	s.v15SessionsMu.Lock()
	sess := s.v15Sessions[sessionID]
	s.v15SessionsMu.Unlock()

	if sess == nil {
		logrus.Warnf("IPMI v1.5 activate session: unknown session ID 0x%08x", sessionID)
		return &goipmi.ActivateSessionResponse{
			CompletionCode: ccInvalidSessionID,
		}
	}

	// Validate the multi-session AuthCode from the session header.
	// Per IPMI spec section 22.17.1:
	//   Multi-session AuthCode = MD5(password[16] | sessionID[4] | ipmi_payload | seq[4] | password[16])
	//   For AuthType=Password: AuthCode = password padded to 16 bytes
	//   For AuthType=None: no validation
	if authType != goipmi.AuthTypeNone {
		expectedAuthCode := computeMultiSessionAuthCode(s.password, sessionID, sequence, ipmiPayload, authType)
		if expectedAuthCode != sessionAuthCode {
			logrus.Warnf("IPMI v1.5 activate session: invalid password for user %q", sess.username)
			return &goipmi.ActivateSessionResponse{
				CompletionCode: ccInvalidAuthCode,
			}
		}
	}

	logrus.Infof("IPMI v1.5 session activated: user=%q sessionID=0x%08x", sess.username, sessionID)

	return &goipmi.ActivateSessionResponse{
		CompletionCode: goipmi.CommandCompleted,
		AuthType:       authType,
		SessionID:      sessionID,
		InboundSeq:     sequence,
		MaxPriv:        goipmi.PrivLevelAdmin,
	}
}

// computeMultiSessionAuthCode computes the IPMI v1.5 multi-session AuthCode.
// Per IPMI spec section 22.17.1:
//
//	AuthCode = MD5(password[16] | sessionID[4] | ipmi_payload[N] | seq[4] | password[16])
//
// For AuthType=Password, the AuthCode is the password padded to 16 bytes.
func computeMultiSessionAuthCode(password string, sessionID uint32, sequence uint32, ipmiPayload []byte, authType uint8) [16]byte {
	key := make([]byte, 16)
	copy(key, password)

	if authType == goipmi.AuthTypePassword {
		var authCode [16]byte
		copy(authCode[:], key)
		return authCode
	}

	// AuthTypeMD5: MD5(password[16] | sessionID[4] | ipmi_payload[N] | seq[4] | password[16])
	inputLen := 16 + 4 + len(ipmiPayload) + 4 + 16
	input := make([]byte, inputLen)

	off := 0
	copy(input[off:], key)
	off += 16
	binary.LittleEndian.PutUint32(input[off:], sessionID)
	off += 4
	copy(input[off:], ipmiPayload)
	off += len(ipmiPayload)
	binary.LittleEndian.PutUint32(input[off:], sequence)
	off += 4
	copy(input[off:], key)

	return md5.Sum(input)
}

// payloadChecksum computes the IPMI payload checksum (Checksum2).
func payloadChecksum(rqAddr, rqSeq, cmd uint8, data []byte) uint8 {
	var c uint8
	c += rqAddr
	c += rqSeq
	c += cmd
	for _, b := range data {
		c += b
	}
	return -c
}

func (s *Simulator) Run() error {
	s.initialize()
	return s.sim.Run()
}

func (s *Simulator) Stop() {
	s.sim.Stop()
	logrus.Info("IPMI simulator gracefully stopped")
}
