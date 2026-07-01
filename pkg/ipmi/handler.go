package ipmi

import (
	"github.com/sirupsen/logrus"
	goipmi "github.com/vmware/goipmi"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

type handler struct {
	rm resourcemanager.ResourceManager
}

func NewHandler(resourceManager resourcemanager.ResourceManager) *handler {
	return &handler{
		rm: resourceManager,
	}
}

func (h *handler) chassisControlHandler(m *goipmi.Message) goipmi.Response {
	r := &goipmi.ChassisControlRequest{}
	if err := m.Request(r); err != nil {
		return err
	}

	var err error

	switch r.ChassisControl {
	case goipmi.ControlPowerDown:
		// Immediate power down (no OS notification), per IPMI spec §28.3.
		logrus.Info("force power off")
		err = h.rm.ForcePowerOff()
	case goipmi.ControlPowerAcpiSoft:
		// ACPI graceful shutdown, per IPMI spec §28.3.
		logrus.Info("power off")
		err = h.rm.PowerOff()
	case goipmi.ControlPowerUp:
		logrus.Info("power on")
		err = h.rm.PowerOn()
	case goipmi.ControlPowerCycle:
		// Power cycle (turns system off then on), per IPMI spec §28.3.
		// This is the most disruptive reset — it cuts power entirely,
		// so we use the force variant.
		logrus.Info("power cycle")
		err = h.rm.ForcePowerCycle()
	case goipmi.ControlPowerHardReset:
		// Hard reset (asserts system reset without power cycling), per IPMI spec §28.3.
		// This is less disruptive than a full power cycle.
		logrus.Info("hard reset")
		err = h.rm.PowerCycle()
	}

	if err != nil {
		logrus.WithError(err).Error("chassis control failed")
		return &goipmi.ChassisControlResponse{
			CompletionCode: goipmi.ErrInvalidState,
		}
	}

	return &goipmi.ChassisControlResponse{
		CompletionCode: goipmi.CommandCompleted,
	}
}

func (h *handler) chassisStatusHandler(*goipmi.Message) goipmi.Response {
	logrus.Info("power status")

	isUp, err := h.rm.GetPowerStatus()
	if err != nil {
		logrus.WithError(err).Error("get power status failed")
		return &goipmi.ChassisStatusResponse{
			CompletionCode: goipmi.ErrInvalidState,
		}
	}
	if !isUp {
		return &goipmi.ChassisStatusResponse{
			CompletionCode: goipmi.CommandCompleted,
			PowerState:     goipmi.PowerOverload,
		}
	}
	return &goipmi.ChassisStatusResponse{
		CompletionCode: goipmi.CommandCompleted,
		PowerState:     goipmi.SystemPower,
	}
}

func (h *handler) setSystemBootOptionsHandler(m *goipmi.Message) goipmi.Response {
	logrus.Info("set boot device")

	r := &goipmi.SetSystemBootOptionsRequest{}
	if err := m.Request(r); err != nil {
		return err
	}

	if r.Param != 5 {
		return &goipmi.SetSystemBootOptionsResponse{
			CompletionCode: goipmi.CommandCompleted,
		}
	}

	var device resourcemanager.BootDevice
	switch r.Data[1] {
	case uint8(goipmi.BootDevicePxe):
		logrus.Infof("set bootdev pxe")
		device = resourcemanager.BootDevicePxe
	case uint8(goipmi.BootDeviceDisk):
		logrus.Infof("set bootdev disk")
		device = resourcemanager.BootDeviceHdd
	case uint8(goipmi.BootDeviceCdrom):
		logrus.Infof("set bootdev cdrom")
		device = resourcemanager.BootDeviceCd
	}

	err := h.rm.SetBootDevice(device)
	if err != nil {
		return &goipmi.SetSystemBootOptionsResponse{
			CompletionCode: goipmi.ErrUnspecified,
		}
	}

	return &goipmi.SetSystemBootOptionsResponse{
		CompletionCode: goipmi.CommandCompleted,
	}
}
