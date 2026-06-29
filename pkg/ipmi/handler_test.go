package ipmi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	goipmi "github.com/vmware/goipmi"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func TestChassisControlHandler(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	handler := NewHandler(mockRM)

	testCases := []struct {
		name           string
		chassisControl goipmi.ChassisControl
		expectedCall   func()
		expectedCode   goipmi.CompletionCode
	}{
		{
			name:           "PowerOn success",
			chassisControl: goipmi.ControlPowerUp,
			expectedCall: func() {
				mockRM.EXPECT().PowerOn().Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:           "PowerOn failure",
			chassisControl: goipmi.ControlPowerUp,
			expectedCall: func() {
				mockRM.EXPECT().PowerOn().Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrInvalidState,
		},
		{
			name:           "ForcePowerOff success",
			chassisControl: goipmi.ControlPowerDown,
			expectedCall: func() {
				mockRM.EXPECT().ForcePowerOff().Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:           "ForcePowerOff failure",
			chassisControl: goipmi.ControlPowerDown,
			expectedCall: func() {
				mockRM.EXPECT().ForcePowerOff().Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrInvalidState,
		},
		{
			name:           "PowerOff (ACPI soft) success",
			chassisControl: goipmi.ControlPowerAcpiSoft,
			expectedCall: func() {
				mockRM.EXPECT().PowerOff().Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:           "PowerOff (ACPI soft) failure",
			chassisControl: goipmi.ControlPowerAcpiSoft,
			expectedCall: func() {
				mockRM.EXPECT().PowerOff().Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrInvalidState,
		},
		{
			name:           "PowerCycle success",
			chassisControl: goipmi.ControlPowerCycle,
			expectedCall: func() {
				mockRM.EXPECT().PowerCycle().Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:           "PowerCycle failure",
			chassisControl: goipmi.ControlPowerCycle,
			expectedCall: func() {
				mockRM.EXPECT().PowerCycle().Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrInvalidState,
		},
		{
			name:           "ForcePowerCycle success",
			chassisControl: goipmi.ControlPowerHardReset,
			expectedCall: func() {
				mockRM.EXPECT().ForcePowerCycle().Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:           "ForcePowerCycle failure",
			chassisControl: goipmi.ControlPowerHardReset,
			expectedCall: func() {
				mockRM.EXPECT().ForcePowerCycle().Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrInvalidState,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.expectedCall()

			message := &goipmi.Message{
				Data: []byte{byte(tc.chassisControl)},
			}

			response := handler.chassisControlHandler(message)

			assert.IsType(t, &goipmi.ChassisControlResponse{}, response)
			res, _ := response.(*goipmi.ChassisControlResponse)
			assert.Equal(t, tc.expectedCode, res.CompletionCode)
		})
	}
}

func TestChassisStatusHandler(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	handler := NewHandler(mockRM)

	testCases := []struct {
		name           string
		expectedStatus uint8
		expectedCall   func()
		expectedCode   goipmi.CompletionCode
	}{
		{
			name:           "PowerStatus on",
			expectedStatus: goipmi.SystemPower,
			expectedCall: func() {
				mockRM.EXPECT().GetPowerStatus().Return(true, nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:           "PowerStatus off",
			expectedStatus: goipmi.PowerOverload,
			expectedCall: func() {
				mockRM.EXPECT().GetPowerStatus().Return(false, nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name: "PowerStatus error",
			expectedCall: func() {
				mockRM.EXPECT().GetPowerStatus().Return(false, fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrInvalidState,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.expectedCall()

			message := &goipmi.Message{}

			response := handler.chassisStatusHandler(message)

			assert.IsType(t, &goipmi.ChassisStatusResponse{}, response)
			res, _ := response.(*goipmi.ChassisStatusResponse)
			assert.Equal(t, tc.expectedCode, res.CompletionCode)
			if tc.expectedStatus != 0 {
				assert.Equal(t, tc.expectedStatus, res.PowerState)
			}
		})
	}
}

func TestSetSystemBootOptionsHandler(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	handler := NewHandler(mockRM)

	testCases := []struct {
		name         string
		bootDevice   goipmi.BootDevice
		expectedCall func()
		expectedCode goipmi.CompletionCode
	}{
		{
			name:       "SetSystemBootOptions with PXE success",
			bootDevice: goipmi.BootDevicePxe,
			expectedCall: func() {
				mockRM.EXPECT().SetBootDevice(gomock.Any()).Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:       "SetSystemBootOptions with PXE failed",
			bootDevice: goipmi.BootDevicePxe,
			expectedCall: func() {
				mockRM.EXPECT().SetBootDevice(gomock.Any()).Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrUnspecified,
		},
		{
			name:       "SetSystemBootOptions with disk success",
			bootDevice: goipmi.BootDeviceDisk,
			expectedCall: func() {
				mockRM.EXPECT().SetBootDevice(gomock.Any()).Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:       "SetSystemBootOptions with disk failed",
			bootDevice: goipmi.BootDeviceDisk,
			expectedCall: func() {
				mockRM.EXPECT().SetBootDevice(gomock.Any()).Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrUnspecified,
		},
		{
			name:       "SetSystemBootOptions with cdrom success",
			bootDevice: goipmi.BootDeviceCdrom,
			expectedCall: func() {
				mockRM.EXPECT().SetBootDevice(gomock.Any()).Return(nil)
			},
			expectedCode: goipmi.CommandCompleted,
		},
		{
			name:       "SetSystemBootOptions with cdrom failed",
			bootDevice: goipmi.BootDeviceCdrom,
			expectedCall: func() {
				mockRM.EXPECT().SetBootDevice(gomock.Any()).Return(fmt.Errorf("error"))
			},
			expectedCode: goipmi.ErrUnspecified,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.expectedCall()

			message := &goipmi.Message{
				Data: []byte{5, uint8(tc.bootDevice), 0, 0, 0, 0},
			}

			response := handler.setSystemBootOptionsHandler(message)

			assert.IsType(t, &goipmi.SetSystemBootOptionsResponse{}, response)
			res, _ := response.(*goipmi.SetSystemBootOptionsResponse)
			assert.Equal(t, tc.expectedCode, res.CompletionCode)
		})
	}
}
