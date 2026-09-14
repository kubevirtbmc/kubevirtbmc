/*
 * Redfish
 *
 * This contains the definition of a Redfish service.
 *
 * API version: 2023.3
 */

package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

type BootOrderRequest struct {
	BootOrder []string `json:"BootOrder"`
}

type APIService struct {
	handler *handler
}

func NewAPIService(bmcUser string, bmcPassword string, resourceManager resourcemanager.ResourceManager) *APIService {
	return &APIService{
		handler: NewHandler(bmcUser, bmcPassword, resourceManager),
	}
}

// RedfishV1Get -
func (s *APIService) RedfishV1Get(ctx context.Context) (server.ImplResponse, error) {
	return server.Response(200, s.handler.GetServiceRoot()), nil
}

// RedfishV1Get_0 serves the trailing-slash alias of RedfishV1Get
// (/redfish/v1/): the generated interface treats the two paths as separate
// operations, but they're the same resource.
func (s *APIService) RedfishV1Get_0(ctx context.Context) (server.ImplResponse, error) {
	return s.RedfishV1Get(ctx)
}

// RedfishV1ManagersGet -
func (s *APIService) RedfishV1ManagersGet(ctx context.Context) (server.ImplResponse, error) {
	return server.Response(200, s.handler.GetManagerCollection()), nil
}

// RedfishV1ManagersManagerIdGet -
func (s *APIService) RedfishV1ManagersManagerIdGet(ctx context.Context, managerId string) (server.ImplResponse, error) {
	manager, err := s.handler.GetManager(ctx)
	if err != nil {
		return server.Response(http.StatusInternalServerError, nil), err
	}

	return server.Response(200, manager), nil
}

// RedfishV1ManagersManagerIdActionsManagerResetPost -
func (s *APIService) RedfishV1ManagersManagerIdActionsManagerResetPost(ctx context.Context, managerId string, managerV1190ResetRequestBody server.ManagerV1190ResetRequestBody) (server.ImplResponse, error) {
	return server.Response(http.StatusNoContent, nil), nil
}

// RedfishV1ManagersManagerIdVirtualMediaGet -
// Deprecated
func (s *APIService) RedfishV1ManagersManagerIdVirtualMediaGet(ctx context.Context, managerId string) (server.ImplResponse, error) {
	return server.Response(200, s.handler.GetVirtualMediaCollection()), nil
}

// RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdGet -
// Deprecated
func (s *APIService) RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdGet(ctx context.Context, managerId string, virtualMediaId string) (server.ImplResponse, error) {
	virtualMedia, err := s.handler.GetVirtualMedia(ctx)
	if err != nil {
		return server.Response(http.StatusInternalServerError, nil), err
	}

	return server.Response(200, virtualMedia), nil
}

// RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdActionsVirtualMediaEjectMediaPost -
// Deprecated
func (s *APIService) RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdActionsVirtualMediaEjectMediaPost(ctx context.Context, managerId string, virtualMediaId string, body map[string]interface{}) (server.ImplResponse, error) {
	// TODO - update RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdActionsVirtualMediaEjectMediaPost with the required logic for this service method.
	// Add api_default_service.go to the .openapi-generator-ignore to avoid overwriting this service implementation when updating open api generation.

	// TODO: Uncomment the next line to return response Response(200, RedfishError{}) or use other options such as http.Ok ...
	// return Response(200, RedfishError{}), nil

	// TODO: Uncomment the next line to return response Response(201, RedfishError{}) or use other options such as http.Ok ...
	// return Response(201, RedfishError{}), nil

	// TODO: Uncomment the next line to return response Response(202, TaskV173Task{}) or use other options such as http.Ok ...
	// return Response(202, TaskV173Task{}), nil

	// TODO: Uncomment the next line to return response Response(204, {}) or use other options such as http.Ok ...
	// return Response(204, nil),nil

	// TODO: Uncomment the next line to return response Response(0, RedfishError{}) or use other options such as http.Ok ...
	// return Response(0, RedfishError{}), nil

	if err := s.handler.VirtualMediaEject(ctx); err != nil {
		return server.Response(http.StatusInternalServerError, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), nil
	}

	return server.Response(204, nil), nil
}

// RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdActionsVirtualMediaInsertMediaPost -
// Deprecated
func (s *APIService) RedfishV1ManagersManagerIdVirtualMediaVirtualMediaIdActionsVirtualMediaInsertMediaPost(ctx context.Context, managerId string, virtualMediaId string, virtualMediaV163InsertMediaRequestBody server.VirtualMediaV163InsertMediaRequestBody) (server.ImplResponse, error) {
	if virtualMediaV163InsertMediaRequestBody.TransferMethod != "" && !virtualMediaV163InsertMediaRequestBody.TransferMethod.IsValid() {
		return server.Response(http.StatusBadRequest, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.PropertyValueNotInList",
						Message:   "The value " + string(virtualMediaV163InsertMediaRequestBody.TransferMethod) + " for the property TransferMethod is not in the list of acceptable values.",
					},
				},
			},
		}), nil
	}

	if virtualMediaV163InsertMediaRequestBody.TransferProtocolType != "" && !virtualMediaV163InsertMediaRequestBody.TransferProtocolType.IsValid() {
		return server.Response(http.StatusBadRequest, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.PropertyValueNotInList",
						Message:   "The value " + string(virtualMediaV163InsertMediaRequestBody.TransferProtocolType) + " for the property TransferProtocolType is not in the list of acceptable values.",
					},
				},
			},
		}), nil
	}

	if err := s.handler.VirtualMediaInsert(ctx, virtualMediaV163InsertMediaRequestBody.Image); err != nil {
		return server.Response(http.StatusInternalServerError, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), nil
	}

	// TODO: In reality, we should return a task object since downloading image from remote is time-consuming.
	// return server.Response(202, server.TaskV173Task{}), nil
	return server.Response(204, nil), nil
}

// RedfishV1SessionServiceSessionsGet -
func (s *APIService) RedfishV1SessionServiceSessionsGet(ctx context.Context) (server.ImplResponse, error) {
	return server.Response(200, s.handler.GetSessionCollection()), nil
}

// RedfishV1SessionServiceSessionsPost -
func (s *APIService) RedfishV1SessionServiceSessionsPost(ctx context.Context, sessionV171Session server.SessionV171Session) (server.ImplResponse, error) {
	username := sessionV171Session.UserName
	password := sessionV171Session.Password

	id, token, err := s.handler.Authenticate(username, password)
	if err != nil {
		return server.Response(http.StatusUnauthorized, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.0.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), err
	}

	location := fmt.Sprintf("/redfish/v1/SessionService/Sessions/%s", id)

	return server.ResponseWithHeaders(201, map[string][]string{
		"X-Auth-Token": {token},
		"Location":     {location},
	}, server.SessionV171Session{
		OdataType: "Session.v1_7_1.Session",
		OdataId:   location,
		Id:        id,
		Name:      "User Session",
		UserName:  username,
	}), nil
}

// RedfishV1SessionServiceSessionsSessionIdGet -
func (s *APIService) RedfishV1SessionServiceSessionsSessionIdGet(ctx context.Context, sessionId string) (server.ImplResponse, error) {
	id, username, err := s.handler.GetSession(sessionId)
	if err != nil {
		return server.Response(http.StatusNotFound, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.0.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), err
	}

	return server.Response(200, server.SessionV171Session{
		OdataType: "#Session.v1_7_1.Session",
		OdataId:   fmt.Sprintf("/redfish/v1/SessionService/Sessions/%s", id),
		Id:        id,
		Name:      "User Session",
		UserName:  &username,
	}), nil
}

// RedfishV1SessionServiceSessionsSessionIdDelete -
func (s *APIService) RedfishV1SessionServiceSessionsSessionIdDelete(ctx context.Context, sessionId string) (server.ImplResponse, error) {
	s.handler.DeleteSession(sessionId)
	return server.Response(204, nil), nil
}

// RedfishV1SystemsGet -
func (s *APIService) RedfishV1SystemsGet(ctx context.Context) (server.ImplResponse, error) {
	return server.Response(200, s.handler.GetComputerSystemCollection()), nil
}

// RedfishV1SystemsComputerSystemIdGet -
func (s *APIService) RedfishV1SystemsComputerSystemIdGet(ctx context.Context, computerSystemId string) (server.ImplResponse, error) {
	computerSystem, err := s.handler.GetComputerSystem(ctx)
	if err != nil {
		return server.Response(http.StatusInternalServerError, nil), err
	}

	// ResetType@Redfish.AllowableValues (kubevirtbmc#204: clients need this
	// to distinguish graceful from force operations) doesn't consistently
	// round-trip into the generated ComputerSystemV1220Reset model --
	// whether it does depends on what redfish.dmtf.org happened to be
	// serving the day generate.sh last ran, which has already broken
	// twice. Advertised here instead, from KubeVirtBMC's own fixed list of
	// supported reset types, by round-tripping the response through a
	// generic map.
	raw, err := json.Marshal(computerSystem)
	if err != nil {
		return server.Response(http.StatusInternalServerError, nil), err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return server.Response(http.StatusInternalServerError, nil), err
	}
	if actions, ok := body["Actions"].(map[string]any); ok {
		if reset, ok := actions["#ComputerSystem.Reset"].(map[string]any); ok {
			reset["ResetType@Redfish.AllowableValues"] = []server.ResourceResetType{
				server.RESOURCERESETTYPE_ON,
				server.RESOURCERESETTYPE_FORCE_OFF,
				server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN,
				server.RESOURCERESETTYPE_GRACEFUL_RESTART,
				server.RESOURCERESETTYPE_FORCE_RESTART,
			}
		}
	}

	return server.Response(200, body), nil
}

// RedfishV1SystemsComputerSystemIdPatch -
func (s *APIService) RedfishV1SystemsComputerSystemIdPatch(ctx context.Context, computerSystemId string, computerSystemV1220ComputerSystem server.ComputerSystemV1220ComputerSystem) (server.ImplResponse, error) {
	if err := s.handler.PatchComputerSystem(ctx, &computerSystemV1220ComputerSystem); err != nil {
		return server.Response(http.StatusInternalServerError, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.0.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), nil
	}

	return server.Response(204, nil), nil
}

// RedfishV1SystemsComputerSystemIdActionsComputerSystemResetPost -
func (s *APIService) RedfishV1SystemsComputerSystemIdActionsComputerSystemResetPost(ctx context.Context, computerSystemId string, computerSystemV1220ResetRequestBody server.ComputerSystemV1220ResetRequestBody) (server.ImplResponse, error) {
	if !computerSystemV1220ResetRequestBody.ResetType.IsValid() {
		return server.Response(http.StatusBadRequest, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.PropertyValueNotInList",
						Message:   "The value " + string(computerSystemV1220ResetRequestBody.ResetType) + " for the property ResetType is not in the list of acceptable values.",
					},
				},
			},
		}), nil
	}

	if err := s.handler.ComputerSystemReset(ctx, computerSystemV1220ResetRequestBody.ResetType); err != nil {
		var retryable *resourcemanager.ErrRetryable
		if errors.As(err, &retryable) {
			// Signal a retryable state the way iLO does: OpenStack sushy
			// (Ironic's Redfish client) retries 5xx responses whose body
			// contains both "iLO" and "InvalidOperationForSystemState":
			//   https://github.com/openstack/sushy/blob/b11baf5f5cd64c01f3245e5efad21236bc0e0a48/sushy/connector.py#L96-L108
			//   https://github.com/openstack/sushy/blob/b11baf5f5cd64c01f3245e5efad21236bc0e0a48/sushy/connector.py#L231-L242
			return server.Response(http.StatusInternalServerError, server.RedfishError{
				Error: server.RedfishErrorError{
					Message: "iLO: InvalidOperationForSystemState - Power transition in progress. Retry later.",
					MessageExtendedInfo: []server.MessageV120Message{
						{
							MessageId: "iLO.2.13.InvalidOperationForSystemState",
							Message:   "InvalidOperationForSystemState: Power transition in progress",
						},
					},
				},
			}), nil
		}
		return server.Response(http.StatusInternalServerError, server.RedfishError{
			Error: server.RedfishErrorError{
				Message: err.Error(),
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), nil
	}

	return server.Response(204, nil), nil
}

// RedfishV1SystemsComputerSystemIdActionsComputerSystemSetDefaultBootOrderPost -
func (s *APIService) RedfishV1SystemsComputerSystemIdActionsComputerSystemSetDefaultBootOrderPost(ctx context.Context, computerSystemId string, body map[string]interface{}) (server.ImplResponse, error) {
	rawBootOrder, ok := body["BootOrder"].([]interface{})
	if !ok {
		return server.Response(http.StatusBadRequest, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.PropertyValueTypeError",
						Message:   "The property BootOrder must be an array of strings.",
					},
				},
			},
		}), nil
	}

	bootOrder := make([]string, len(rawBootOrder))
	for i, v := range rawBootOrder {
		str, ok := v.(string)
		if !ok {
			return server.Response(http.StatusBadRequest, server.RedfishError{
				Error: server.RedfishErrorError{
					MessageExtendedInfo: []server.MessageV120Message{
						{
							MessageId: "Base.1.2.PropertyValueTypeError",
							Message:   "The property BootOrder must only contain strings.",
						},
					},
				},
			}), nil
		}
		bootOrder[i] = str
	}

	if err := s.handler.ComputerSystemSetDefaultBootOrder(ctx, bootOrder); err != nil {
		return server.Response(http.StatusInternalServerError, server.RedfishError{
			Error: server.RedfishErrorError{
				MessageExtendedInfo: []server.MessageV120Message{
					{
						MessageId: "Base.1.2.GeneralError",
						Message:   err.Error(),
					},
				},
			},
		}), nil
	}

	return server.Response(204, nil), nil
}

// RedfishV1SystemsComputerSystemIdOperatingSystemGet -
func (s *APIService) RedfishV1SystemsComputerSystemIdOperatingSystemGet(ctx context.Context, computerSystemId string) (server.ImplResponse, error) {
	operatingSystem := server.OperatingSystemV101OperatingSystem{
		OdataContext: "/redfish/v1/$metadata#OperatingSystem.OperatingSystem",
		OdataId:      "/redfish/v1/Systems/1/OperatingSystem",
		OdataType:    "#OperatingSystem.v1_0_1.OperatingSystem",
		Name:         "Operating System",
		Id:           "1",
	}

	return server.Response(200, operatingSystem), nil
}
