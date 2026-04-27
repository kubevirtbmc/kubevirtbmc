package resourcemanager

import (
	"fmt"
	"time"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type ManagerInterface interface {
	OdataInterface

	Id() string
}

type ManagerAdapter struct {
	manager *server.ManagerV1190Manager
}

func NewManager(id, name string) *ManagerAdapter {
	generatedManager := &server.ManagerV1190Manager{
		OdataContext: "/redfish/v1/$metadata#Manager.Manager",
		OdataId:      fmt.Sprintf("/redfish/v1/Managers/%s", id),
		OdataType:    "#Manager.v1_19_2.Manager",
		Description:  "Manager",
		Name:         name,
		Id:           id,
		UUID:         "00000000-0000-0000-0000-000000000000",
		Model:        util.Ptr("KubeVirtBMC"),
		Status:       server.ResourceStatus{},
		ManagerType:  "BMC",
		Links:        server.ManagerV1190Links{},
		Actions: server.ManagerV1190Actions{
			ManagerReset: server.ManagerV1190Reset{
				Target: fmt.Sprintf("/redfish/v1/Managers/%s/Actions/Manager.Reset", id),
				Title:  "Reset",
			},
		},
		DateTime: util.Ptr(time.Now()),
		EthernetInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/EthernetInterfaces", id),
		},
		LogServices: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/LogServices", id),
		},
		SerialInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/SerialInterfaces", id),
		},
		VirtualMedia: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/VirtualMedia", id),
		},
	}

	return &ManagerAdapter{manager: generatedManager}
}

func (a *ManagerAdapter) Id() string {
	return a.manager.Id
}

func (a *ManagerAdapter) OdataId() string {
	return a.manager.OdataId
}

func (a *ManagerAdapter) Manage(resource OdataInterface) error {
	a.manager.Links.ManagerForServers = append(a.manager.Links.ManagerForServers, server.OdataV4IdRef{
		OdataId: resource.OdataId(),
	})

	return nil
}

func (a *ManagerAdapter) ManagedBy(resource OdataInterface) error {
	panic("implement me")
}

func (a *ManagerAdapter) Manager() *server.ManagerV1190Manager {
	return a.manager
}
