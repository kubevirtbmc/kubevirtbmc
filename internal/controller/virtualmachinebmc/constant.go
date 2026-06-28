package virtualmachinebmc

const (
	DefaultUsername            = "admin"
	DefaultPassword            = "password"
	virtBMCContainerName       = "virtbmc"
	VirtBMCImageName           = "kubevirtbmc/virtbmc"
	ipmiPort                   = 10623
	redfishPort                = 10080
	IPMISvcPort                = 623
	RedfishSvcPort             = 80
	ipmiPortName               = "ipmi"
	redfishPortName            = "redfish"
	VirtualMachineBMCNameLabel = "kubevirt.io/virtualmachinebmc-name"
	VMNameLabel                = "kubevirt.io/vm-name"
	VirtualMachineBMCNamespace = "kubevirtbmc-system"
	EnableIPMIAnnotation       = "bmc.kubevirt.io/enable-ipmi"
)
