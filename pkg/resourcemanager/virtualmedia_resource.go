package resourcemanager

import (
	"fmt"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type VirtualMediaInterface interface {
	OdataInterface

	Id() string
	SetVirtualMedia(string, bool)
}

type VirtualMediaAdapter struct {
	virtualMedia *server.VirtualMediaV163VirtualMedia
}

func NewVirtualMedia(id, name string) *VirtualMediaAdapter {
	odataId := fmt.Sprintf("/redfish/v1/Managers/BMC/VirtualMedia/%s", id)
	generatedVirtualMedia := &server.VirtualMediaV163VirtualMedia{
		OdataContext: "/redfish/v1/$metadata#VirtualMedia.VirtualMedia",
		OdataId:      odataId,
		OdataType:    "#VirtualMedia.v1_6_3.VirtualMedia",
		Actions: server.VirtualMediaV163Actions{
			VirtualMediaEjectMedia: server.VirtualMediaV163EjectMedia{
				Target: fmt.Sprintf("%s/Actions/VirtualMedia.EjectMedia", odataId),
				Title:  "EjectMedia",
			},
			VirtualMediaInsertMedia: server.VirtualMediaV163InsertMedia{
				Target: fmt.Sprintf("%s/Actions/VirtualMedia.InsertMedia", odataId),
				Title:  "InsertMedia",
			},
		},
		ConnectedVia: server.VIRTUALMEDIAV163CONNECTEDVIA_NOT_CONNECTED,
		Description:  "Virtual Media",
		Name:         name,
		Id:           id,
		Image:        util.Ptr(""),
		ImageName:    util.Ptr(""),
		Inserted:     util.Ptr(false),
		MediaTypes: []server.VirtualMediaV163MediaType{
			"CD",
			"DVD",
		},
		WriteProtected: util.Ptr(false),
	}

	return &VirtualMediaAdapter{virtualMedia: generatedVirtualMedia}
}

func (a *VirtualMediaAdapter) Id() string {
	return a.virtualMedia.Id
}

func (a *VirtualMediaAdapter) OdataId() string {
	return a.virtualMedia.OdataId
}

func (a *VirtualMediaAdapter) Manage(resource OdataInterface) error {
	panic("implement me")
}

func (a *VirtualMediaAdapter) ManagedBy(resource OdataInterface) error {
	panic("implement me")
}

func (a *VirtualMediaAdapter) VirtualMedia() *server.VirtualMediaV163VirtualMedia {
	return a.virtualMedia
}

func (a *VirtualMediaAdapter) SetVirtualMedia(imageURL string, inserted bool) {
	a.virtualMedia.Image = util.Ptr(imageURL)
	a.virtualMedia.Inserted = util.Ptr(inserted)
	if inserted {
		a.virtualMedia.ConnectedVia = server.VIRTUALMEDIAV163CONNECTEDVIA_URI
	} else {
		a.virtualMedia.ConnectedVia = server.VIRTUALMEDIAV163CONNECTEDVIA_NOT_CONNECTED
	}
}
