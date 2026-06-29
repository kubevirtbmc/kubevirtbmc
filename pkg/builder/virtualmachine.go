package builder

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

// VirtualMachineBuilder builds a VirtualMachine object.
type VirtualMachineBuilder struct {
	vm *kubevirtv1.VirtualMachine
}

func NewVirtualMachineBuilder(namespace, name string) *VirtualMachineBuilder {
	return &VirtualMachineBuilder{
		vm: &kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
		},
	}
}

func (b *VirtualMachineBuilder) Build() *kubevirtv1.VirtualMachine {
	return b.vm
}

func (b *VirtualMachineBuilder) WithTemplate() *VirtualMachineBuilder {
	b.vm.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
	return b
}

func (b *VirtualMachineBuilder) Running(running bool) *VirtualMachineBuilder {
	b.vm.Spec.Running = &running
	return b
}

func (b *VirtualMachineBuilder) RunStrategy(strategy kubevirtv1.VirtualMachineRunStrategy) *VirtualMachineBuilder {
	b.vm.Spec.RunStrategy = &strategy
	return b
}

func (b *VirtualMachineBuilder) WithDisk(name string, bootOrder *uint) *VirtualMachineBuilder {
	disk := kubevirtv1.Disk{
		Name: name,
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{},
		},
	}
	if bootOrder != nil {
		disk.BootOrder = bootOrder
	}

	if b.vm.Spec.Template == nil {
		b.WithTemplate()
	}
	b.vm.Spec.Template.Spec.Domain.Devices.Disks = append(b.vm.Spec.Template.Spec.Domain.Devices.Disks, disk)

	return b
}

func (b *VirtualMachineBuilder) WithCDRomDisk(name string, bootOrder *uint) *VirtualMachineBuilder {
	disk := kubevirtv1.Disk{
		Name: name,
		DiskDevice: kubevirtv1.DiskDevice{
			CDRom: &kubevirtv1.CDRomTarget{},
		},
	}
	if bootOrder != nil {
		disk.BootOrder = bootOrder
	}

	if b.vm.Spec.Template == nil {
		b.WithTemplate()
	}
	b.vm.Spec.Template.Spec.Domain.Devices.Disks = append(b.vm.Spec.Template.Spec.Domain.Devices.Disks, disk)

	return b
}

func (b *VirtualMachineBuilder) WithInterface(name string, bootOrder *uint) *VirtualMachineBuilder {
	intf := kubevirtv1.Interface{
		Name: name,
	}
	if bootOrder != nil {
		intf.BootOrder = bootOrder
	}

	if b.vm.Spec.Template == nil {
		b.vm.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
	}
	b.vm.Spec.Template.Spec.Domain.Devices.Interfaces = append(b.vm.Spec.Template.Spec.Domain.Devices.Interfaces, intf)

	return b
}

func (b *VirtualMachineBuilder) WithVolumes(volumes ...kubevirtv1.Volume) *VirtualMachineBuilder {
	if b.vm.Spec.Template == nil {
		b.WithTemplate()
	}

	if b.vm.Spec.Template.Spec.Volumes == nil {
		b.vm.Spec.Template.Spec.Volumes = make([]kubevirtv1.Volume, 0, len(volumes))
	}
	b.vm.Spec.Template.Spec.Volumes = append(b.vm.Spec.Template.Spec.Volumes, volumes...)

	return b
}

func (b *VirtualMachineBuilder) Ready(ready bool) *VirtualMachineBuilder {
	b.vm.Status.Ready = ready
	return b
}

func (b *VirtualMachineBuilder) WithStateChangeRequests(requests ...kubevirtv1.VirtualMachineStateChangeRequest) *VirtualMachineBuilder {
	b.vm.Status.StateChangeRequests = append(b.vm.Status.StateChangeRequests, requests...)
	return b
}

// VirtualMachineInstanceBuilder builds a VirtualMachineInstance object.
type VirtualMachineInstanceBuilder struct {
	vmi *kubevirtv1.VirtualMachineInstance
}

func NewVirtualMachineInstanceBuilder(namespace, name string) *VirtualMachineInstanceBuilder {
	return &VirtualMachineInstanceBuilder{
		vmi: &kubevirtv1.VirtualMachineInstance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
		},
	}
}

func (b *VirtualMachineInstanceBuilder) Phase(phase kubevirtv1.VirtualMachineInstancePhase) *VirtualMachineInstanceBuilder {
	b.vmi.Status.Phase = phase
	return b
}

func (b *VirtualMachineInstanceBuilder) Build() *kubevirtv1.VirtualMachineInstance {
	return b.vmi
}
