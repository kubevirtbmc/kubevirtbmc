package virtualmachinebmc

const (
	DefaultUsername            = "admin"
	DefaultPassword            = "password"
	DefaultAgentCPURequest     = "10m"
	DefaultAgentMemoryRequest  = "128Mi"
	virtBMCContainerName       = "virtbmc"
	VirtBMCImageName           = "kubevirtbmc/virtbmc"
	ipmiPort                   = 10623
	redfishPort                = 10080
	IPMISvcPort                = 623
	RedfishSvcPort             = 80
	ipmiPortName               = "ipmi"
	redfishPortName            = "redfish"
	VirtualMachineBMCNamespace = "kubevirtbmc-system"
	EnableIPMIAnnotation       = "bmc.kubevirt.io/enable-ipmi"
	SecretHashAnnotation       = "bmc.kubevirt.io/secret-hash"
)
