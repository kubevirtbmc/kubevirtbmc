package ipmi

import (
	"crypto/md5"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	goipmi "github.com/vmware/goipmi"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func TestProcessSessionChallengeValidUsername(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "password")

	response := sim.processSessionChallenge("admin")

	res, ok := response.(*goipmi.SessionChallengeResponse)
	assert.True(t, ok, "expected *SessionChallengeResponse, got %T", response)
	assert.Equal(t, goipmi.CommandCompleted, res.CompletionCode)
	assert.NotZero(t, res.TemporarySessionID)
	assert.NotEqual(t, [16]byte{}, res.Challenge, "challenge should not be zero")
}

func TestProcessSessionChallengeInvalidUsername(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "password")

	response := sim.processSessionChallenge("attacker")

	res, ok := response.(*goipmi.SessionChallengeResponse)
	assert.True(t, ok, "expected *SessionChallengeResponse, got %T", response)
	assert.Equal(t, ccInvalidAuthCode, res.CompletionCode)
}

// buildActivateIPMIPayload constructs the ipmiPayload for multi-session AuthCode validation.
// Reconstructs: RsAddr | NetFnRsLUN | Checksum1 | RqAddr | RqSeq | Command | Data | Checksum2
func buildActivateIPMIPayload(req *goipmi.ActivateSessionRequest) []byte {
	// Serialize the ActivateSessionRequest to get the Data portion
	data := make([]byte, 1+1+16+4)
	data[0] = req.AuthType
	data[1] = req.PrivLevel
	copy(data[2:18], req.AuthCode[:])
	binary.LittleEndian.PutUint32(data[18:22], binary.LittleEndian.Uint32(req.InSeq[:]))

	rqAddr := uint8(0x81) // remoteSWID
	rqSeq := uint8(0x08)  // rqSeq << 2 | lun
	cmd := uint8(goipmi.CommandActivateSession)

	ipmiPayload := make([]byte, 7+len(data))
	ipmiPayload[0] = 0x20 // RsAddr (bmcSlaveAddr)
	ipmiPayload[1] = 0x18 // NetFnRsLUN (App netfn << 2 | lun)
	ipmiPayload[2] = goipmiChecksum(0x20, 0x18)
	ipmiPayload[3] = rqAddr
	ipmiPayload[4] = rqSeq
	ipmiPayload[5] = cmd
	copy(ipmiPayload[6:], data)
	// Checksum2 = -(rqAddr + rqSeq + cmd + sum(data))
	var cs2 uint8
	cs2 += rqAddr
	cs2 += rqSeq
	cs2 += cmd
	for _, b := range data {
		cs2 += b
	}
	ipmiPayload[6+len(data)] = -cs2
	return ipmiPayload
}

func goipmiChecksum(b ...uint8) uint8 {
	var c uint8
	for _, x := range b {
		c += x
	}
	return -c
}

func TestProcessActivateSessionValidPasswordMD5(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "testpass")

	// Step 1: Get a session challenge
	challengeRes := sim.processSessionChallenge("admin")
	challengeResp := challengeRes.(*goipmi.SessionChallengeResponse)
	assert.Equal(t, goipmi.CommandCompleted, challengeResp.CompletionCode)

	// Step 2: Build ActivateSession request and compute the multi-session AuthCode
	authType := uint8(goipmi.AuthTypeMD5)
	sequence := uint32(0)

	// Build the ActivateSessionRequest to construct the IPMI payload
	activateReq := &goipmi.ActivateSessionRequest{
		AuthType:  authType,
		PrivLevel: goipmi.PrivLevelAdmin,
	}
	binary.LittleEndian.PutUint32(activateReq.InSeq[:], sequence)
	ipmiPayload := buildActivateIPMIPayload(activateReq)

	// Compute the expected multi-session AuthCode
	expectedAuthCode := computeMultiSessionAuthCode("testpass", challengeResp.TemporarySessionID, sequence, ipmiPayload, authType)

	activateRes := sim.processActivateSession(challengeResp.TemporarySessionID, authType, sequence, expectedAuthCode, ipmiPayload)
	activateResp, ok := activateRes.(*goipmi.ActivateSessionResponse)
	assert.True(t, ok, "expected *ActivateSessionResponse, got %T", activateRes)
	assert.Equal(t, goipmi.CommandCompleted, activateResp.CompletionCode)
	assert.Equal(t, uint8(goipmi.PrivLevelAdmin), activateResp.MaxPriv)
}

func TestProcessActivateSessionInvalidPasswordMD5(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "correctpass")

	// Step 1: Get a session challenge
	challengeRes := sim.processSessionChallenge("admin")
	challengeResp := challengeRes.(*goipmi.SessionChallengeResponse)
	assert.Equal(t, goipmi.CommandCompleted, challengeResp.CompletionCode)

	// Step 2: Compute AuthCode with WRONG password
	authType := uint8(goipmi.AuthTypeMD5)
	sequence := uint32(0)

	activateReq := &goipmi.ActivateSessionRequest{
		AuthType:  authType,
		PrivLevel: goipmi.PrivLevelAdmin,
	}
	ipmiPayload := buildActivateIPMIPayload(activateReq)
	wrongAuthCode := computeMultiSessionAuthCode("wrongpass", challengeResp.TemporarySessionID, sequence, ipmiPayload, authType)

	activateRes := sim.processActivateSession(challengeResp.TemporarySessionID, authType, sequence, wrongAuthCode, ipmiPayload)
	activateResp := activateRes.(*goipmi.ActivateSessionResponse)
	assert.Equal(t, ccInvalidAuthCode, activateResp.CompletionCode,
		"should reject session activation with wrong password")
}

func TestProcessActivateSessionValidPasswordAuthTypePassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "testpass")

	// Step 1: Get a session challenge
	challengeRes := sim.processSessionChallenge("admin")
	challengeResp := challengeRes.(*goipmi.SessionChallengeResponse)
	assert.Equal(t, goipmi.CommandCompleted, challengeResp.CompletionCode)

	// Step 2: Activate with Password auth type - auth code is the password padded to 16 bytes
	authType := uint8(goipmi.AuthTypePassword)
	sequence := uint32(0)

	activateReq := &goipmi.ActivateSessionRequest{
		AuthType:  authType,
		PrivLevel: goipmi.PrivLevelAdmin,
	}
	ipmiPayload := buildActivateIPMIPayload(activateReq)
	expectedAuthCode := computeMultiSessionAuthCode("testpass", challengeResp.TemporarySessionID, sequence, ipmiPayload, authType)

	activateRes := sim.processActivateSession(challengeResp.TemporarySessionID, authType, sequence, expectedAuthCode, ipmiPayload)
	activateResp := activateRes.(*goipmi.ActivateSessionResponse)
	assert.Equal(t, goipmi.CommandCompleted, activateResp.CompletionCode)
}

func TestProcessActivateSessionWrongPasswordAuthTypePassword(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "correctpass")

	// Step 1: Get a session challenge
	challengeRes := sim.processSessionChallenge("admin")
	challengeResp := challengeRes.(*goipmi.SessionChallengeResponse)
	assert.Equal(t, goipmi.CommandCompleted, challengeResp.CompletionCode)

	// Step 2: Activate with WRONG password
	authType := uint8(goipmi.AuthTypePassword)
	sequence := uint32(0)

	activateReq := &goipmi.ActivateSessionRequest{
		AuthType:  authType,
		PrivLevel: goipmi.PrivLevelAdmin,
	}
	ipmiPayload := buildActivateIPMIPayload(activateReq)
	wrongAuthCode := computeMultiSessionAuthCode("wrongpass", challengeResp.TemporarySessionID, sequence, ipmiPayload, authType)

	activateRes := sim.processActivateSession(challengeResp.TemporarySessionID, authType, sequence, wrongAuthCode, ipmiPayload)
	activateResp := activateRes.(*goipmi.ActivateSessionResponse)
	assert.Equal(t, ccInvalidAuthCode, activateResp.CompletionCode,
		"should reject session activation with wrong password (AuthType=Password)")
}

func TestProcessActivateSessionUnknownSessionID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "password")

	activateRes := sim.processActivateSession(0xdeadbeef, goipmi.AuthTypeMD5, 0, [16]byte{}, nil)
	activateResp := activateRes.(*goipmi.ActivateSessionResponse)
	assert.Equal(t, ccInvalidSessionID, activateResp.CompletionCode,
		"should reject unknown session ID")
}

func TestComputeMultiSessionAuthCodeMD5(t *testing.T) {
	password := "testpass"
	var sessionID uint32 = 0x12345678
	var sequence uint32 = 0x00000001
	ipmiPayload := []byte{0x20, 0x18, 0xc8, 0x81, 0x08, 0x3a, 0x02, 0x04}
	authType := uint8(goipmi.AuthTypeMD5)

	authCode := computeMultiSessionAuthCode(password, sessionID, sequence, ipmiPayload, authType)

	// Manually compute expected
	key := make([]byte, 16)
	copy(key, password)
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
	expected := md5.Sum(input)

	assert.Equal(t, expected, authCode)
}

func TestComputeMultiSessionAuthCodePasswordAuthType(t *testing.T) {
	password := "mypassword"
	authCode := computeMultiSessionAuthCode(password, 0, 0, nil, goipmi.AuthTypePassword)

	// For AuthType=Password, the auth code is the password padded to 16 bytes
	expected := [16]byte{}
	copy(expected[:], password)
	assert.Equal(t, expected, authCode)
}

func TestProcessActivateSessionAuthTypeNoneSkipsValidation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	sim := NewSimulator("127.0.0.1", 623, mockRM, "admin", "password")

	// Step 1: Get a session challenge
	challengeRes := sim.processSessionChallenge("admin")
	challengeResp := challengeRes.(*goipmi.SessionChallengeResponse)
	assert.Equal(t, goipmi.CommandCompleted, challengeResp.CompletionCode)

	// Step 2: Activate with AuthType=None - should skip validation entirely
	activateRes := sim.processActivateSession(challengeResp.TemporarySessionID, goipmi.AuthTypeNone, 0, [16]byte{}, nil)
	activateResp := activateRes.(*goipmi.ActivateSessionResponse)
	assert.Equal(t, goipmi.CommandCompleted, activateResp.CompletionCode)
}
