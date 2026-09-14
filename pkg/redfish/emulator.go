package redfish

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

type Emulator struct {
	ctx  context.Context
	port int

	bmcUser     string
	bmcPassword string

	wg     sync.WaitGroup
	server *http.Server
}

// newRouter builds the full Redfish route table, with authentication
// required on everything but the public routes. hack/redfish/trim-redfish-spec
// strips the OpenAPI spec down to hack/redfish/spec/implemented-operations.yaml
// before codegen, so the generated DefaultAPIServicer interface — and the
// route table server.NewDefaultAPIController builds from it — never carries
// the ~4000 stub operations the full DMTF spec would otherwise generate.
func newRouter(bmcUser, bmcPassword string, resourceManager resourcemanager.ResourceManager) http.Handler {
	apiService := NewAPIService(bmcUser, bmcPassword, resourceManager)
	apiController := server.NewDefaultAPIController(apiService, server.WithDefaultAPIErrorHandler(recordingErrorHandler))
	return server.NewRouter(authFilter{
		inner:      apiController,
		middleware: session.AuthMiddleware(bmcUser, bmcPassword),
	})
}

func NewEmulator(ctx context.Context, port int, bmcUser string, bmcPassword string, resourceManager resourcemanager.ResourceManager) *Emulator {
	router := newRouter(bmcUser, bmcPassword, resourceManager)

	// Mount /healthz outside the access-log wrapper so readiness probes stay silent.
	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	root.Handle("/", accessLog(router))

	return &Emulator{
		ctx:         ctx,
		port:        port,
		bmcUser:     bmcUser,
		bmcPassword: bmcPassword,
		server: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: root,
		},
	}
}

func (e *Emulator) Run() error {
	e.wg.Add(1)

	go func() {
		defer e.wg.Done()

		if err := e.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Println(err)
		}
	}()

	return nil
}

func (e *Emulator) Stop() {
	if err := e.server.Shutdown(e.ctx); err != nil {
		fmt.Println(err)
	}
	e.wg.Wait()
	logrus.Info("Redfish emulator gracefully stopped")
}

// publicRoutes are the routes a Redfish client must be able to reach before
// it has a session: discovering the service root, and creating the session
// itself. Everything else requires a valid session or basic-auth
// credentials. This mirrors the auth split the OpenAPI generator's go-server
// template used to hard-code directly into routers.go; that file is now
// fully generated (and regenerated), so the split lives here instead, where
// a generator upgrade can't silently drop it.
var publicRoutes = map[string]bool{
	"RedfishV1Get":                        true,
	"RedfishV1SessionServiceSessionsPost": true,
}

// authFilter requires a valid session for every route except publicRoutes.
type authFilter struct {
	inner      server.Router
	middleware func(http.Handler) http.Handler
}

func (f authFilter) Routes() server.Routes {
	routes := f.inner.Routes()
	wrapped := make(server.Routes, len(routes))
	for name, route := range routes {
		wrapped[name] = f.wrap(route)
	}
	return wrapped
}

func (f authFilter) OrderedRoutes() []server.Route {
	ordered := f.inner.OrderedRoutes()
	wrapped := make([]server.Route, len(ordered))
	for i, route := range ordered {
		wrapped[i] = f.wrap(route)
	}
	return wrapped
}

func (f authFilter) wrap(route server.Route) server.Route {
	if publicRoutes[baseRouteName(route.Name)] {
		return route
	}
	route.HandlerFunc = f.middleware(route.HandlerFunc).ServeHTTP
	return route
}

// baseRouteName strips the _N suffix the OpenAPI generator appends to alias
// paths of the same operation (e.g. RedfishV1Get_0 -> RedfishV1Get), so alias
// routes inherit the public/session-required status of their canonical route.
func baseRouteName(name string) string {
	i := strings.LastIndexByte(name, '_')
	if i < 0 || i+1 == len(name) {
		return name
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return name
		}
	}
	return name[:i]
}
