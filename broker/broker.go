package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/cloudfoundry-community/cf-esb/config"
	cfclient "github.com/cloudfoundry/go-cfclient/v3/client"
	"github.com/cloudfoundry/go-cfclient/v3/resource"
	"github.com/go-chi/chi/v5"
)

const osbAPIVersion = "2.16"
const appStartOperation = "app-start"

var instanceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type Broker struct {
	services  *config.File
	capi      capi
	spaceGUID string
}

type hostResolver interface {
	ServiceHost(context.Context, *resource.App, config.Service) (string, error)
}

func New(services *config.File, cf capi, spaceGUID string) *Broker {
	return &Broker{services: services, capi: cf, spaceGUID: spaceGUID}
}

func Handler(b *Broker, username, password string) http.Handler {
	router := chi.NewRouter()
	router.Use(osbVersionMiddleware)
	router.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	router.Group(func(protected chi.Router) {
		protected.Use(basicAuth(username, password))
		protected.Get("/v2/catalog", b.catalog)
		protected.Put("/v2/service_instances/{instance_id}", b.provision)
		protected.Delete("/v2/service_instances/{instance_id}", b.deprovision)
		protected.Get("/v2/service_instances/{instance_id}/last_operation", b.lastOperation)
		protected.Put("/v2/service_instances/{instance_id}/service_bindings/{binding_id}", b.bind)
		protected.Delete("/v2/service_instances/{instance_id}/service_bindings/{binding_id}", b.unbind)
		protected.Get("/v2/service_instances/{instance_id}/service_bindings/{binding_id}/last_operation", b.bindingLastOperation)
	})
	return router
}

func (b *Broker) catalog(w http.ResponseWriter, _ *http.Request) {
	result := catalog{Services: make([]catalogService, 0, len(b.services.Services))}
	for _, service := range b.services.Services {
		entry := catalogService{ID: service.ID, Name: service.Name, Description: service.Description, Bindable: service.Bindable, Tags: service.Tags}
		for _, plan := range service.Plans {
			entry.Plans = append(entry.Plans, catalogPlan{ID: plan.ID, Name: plan.Name, Description: plan.Description, Free: plan.Free})
		}
		result.Services = append(result.Services, entry)
	}
	writeJSON(w, http.StatusOK, result)
}

func (b *Broker) provision(w http.ResponseWriter, r *http.Request) {
	instanceID := chi.URLParam(r, "instance_id")
	if !instanceIDPattern.MatchString(instanceID) {
		writeError(w, http.StatusBadRequest, "BadRequest", "invalid service instance id")
		return
	}
	var request provisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	service, _, ok := b.servicePlan(w, request.ServiceID, request.PlanID)
	if !ok {
		return
	}
	if b.spaceGUID == "" {
		writeError(w, http.StatusInternalServerError, "ConfigurationError", "broker space is not configured")
		return
	}
	appName := appName(service.ID, instanceID)
	if len(appName) > 50 {
		writeError(w, http.StatusBadRequest, "BadRequest", "service instance id produces a Cloud Foundry app name longer than 50 characters")
		return
	}
	app, err := b.capi.FindServiceApp(r.Context(), appName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if app != nil {
		if app.Relationships.Space.Data == nil || app.Relationships.Space.Data.GUID != b.spaceGUID {
			writeError(w, http.StatusConflict, "Conflict", "service app exists outside configured broker space")
			return
		}
		if !strings.EqualFold(app.State, "STARTED") {
			if err := b.capi.StartServiceApp(r.Context(), app.GUID); err != nil {
				writeError(w, http.StatusInternalServerError, "CFError", err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]string{"operation": appStartOperation})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	app, err = b.capi.CreateServiceApp(r.Context(), appName, b.spaceGUID, service)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if err = b.capi.StartServiceApp(r.Context(), app.GUID); err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", fmt.Sprintf("created app %s but could not start it: %v", appName, err))
		return
	}
	if r.URL.Query().Get("accepts_incomplete") != "true" {
		writeError(w, http.StatusUnprocessableEntity, "AsyncRequired", "service image staging is asynchronous; retry with accepts_incomplete=true")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation": appStartOperation})
}

func (b *Broker) deprovision(w http.ResponseWriter, r *http.Request) {
	request := provisionQuery{ServiceID: r.URL.Query().Get("service_id"), PlanID: r.URL.Query().Get("plan_id")}
	if request.ServiceID == "" || request.PlanID == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "service_id and plan_id query parameters are required")
		return
	}
	service, _, ok := b.servicePlan(w, request.ServiceID, request.PlanID)
	if !ok {
		return
	}
	app, err := b.capi.FindServiceApp(r.Context(), appName(service.ID, chi.URLParam(r, "instance_id")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if app == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if jobGUID := b.capi.ServiceDeletionJob(app); jobGUID != "" {
		state, _, statusErr := b.capi.ServiceDeletionStatus(r.Context(), jobGUID)
		if statusErr != nil {
			writeError(w, http.StatusInternalServerError, "CFError", statusErr.Error())
			return
		}
		switch state {
		case "succeeded":
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		case "failed":
			// Allow a new CAPI deletion job after a prior job failed.
		default:
			writeJSON(w, http.StatusAccepted, map[string]string{"operation": jobGUID})
			return
		}
	}
	if r.URL.Query().Get("accepts_incomplete") != "true" {
		writeError(w, http.StatusUnprocessableEntity, "AsyncRequired", "service app deletion is asynchronous; retry with accepts_incomplete=true")
		return
	}
	if err = b.capi.DeletePoliciesForDestination(r.Context(), app.GUID, service.Port); err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	jobGUID, err := b.capi.DeleteServiceApp(r.Context(), app.GUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if jobGUID == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	// The job GUID is returned directly as the OSB operation. Persist it on the
	// app for retry safety, but do not turn an already-enqueued delete into a
	// synchronous failure if metadata update races with app removal.
	_ = b.capi.RememberServiceDeletionJob(r.Context(), app.GUID, jobGUID)
	writeJSON(w, http.StatusAccepted, map[string]string{"operation": jobGUID})
}

func (b *Broker) lastOperation(w http.ResponseWriter, r *http.Request) {
	request := struct {
		ServiceID string
		PlanID    string
	}{ServiceID: r.URL.Query().Get("service_id"), PlanID: r.URL.Query().Get("plan_id")}
	service, _, ok := b.servicePlan(w, request.ServiceID, request.PlanID)
	if !ok {
		return
	}
	app, err := b.capi.FindServiceApp(r.Context(), appName(service.ID, chi.URLParam(r, "instance_id")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if app == nil {
		if operation := r.URL.Query().Get("operation"); operation != "" && operation != appStartOperation {
			writeJSON(w, http.StatusOK, lastOperationResponse{State: "succeeded", Description: "service app deletion completed"})
			return
		}
		writeError(w, http.StatusNotFound, "NotFound", "service instance not found")
		return
	}
	if operation := r.URL.Query().Get("operation"); operation != "" && operation != appStartOperation {
		state, description, err := b.capi.ServiceDeletionStatus(r.Context(), operation)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "CFError", err.Error())
			return
		}
		if state == "succeeded" {
			state, description = "in progress", "Cloud Foundry delete job completed; waiting for service app removal"
		}
		writeJSON(w, http.StatusOK, lastOperationResponse{State: state, Description: description})
		return
	}
	state, description := "in progress", "waiting for service app to start"
	if strings.EqualFold(app.State, "STARTED") {
		state, description = "succeeded", "service app is running"
	}
	writeJSON(w, http.StatusOK, lastOperationResponse{State: state, Description: description})
}

func (b *Broker) bind(w http.ResponseWriter, r *http.Request) {
	var request bindRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	service, _, ok := b.servicePlan(w, request.ServiceID, request.PlanID)
	if !ok {
		return
	}
	if !service.Bindable {
		writeError(w, http.StatusUnprocessableEntity, "UnprocessableEntity", "service is not bindable")
		return
	}
	appGUID := request.BindResource.AppGUID
	if appGUID == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "bind_resource.app_guid is required")
		return
	}
	serviceApp, err := b.capi.FindServiceApp(r.Context(), appName(service.ID, chi.URLParam(r, "instance_id")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if serviceApp == nil {
		writeError(w, http.StatusNotFound, "NotFound", "service instance not found")
		return
	}
	if !strings.EqualFold(serviceApp.State, "STARTED") {
		writeError(w, http.StatusConflict, "Conflict", "service app is not running yet")
		return
	}
	if err = b.capi.CreatePolicy(r.Context(), appGUID, serviceApp.GUID, service.Port); err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if err = b.capi.RememberBinding(r.Context(), serviceApp.GUID, chi.URLParam(r, "binding_id"), appGUID); err != nil {
		_ = b.capi.DeletePolicy(r.Context(), appGUID, serviceApp.GUID, service.Port)
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	resolver, ok := b.capi.(hostResolver)
	if !ok {
		writeError(w, http.StatusInternalServerError, "ConfigurationError", "CAPI client cannot resolve service host")
		return
	}
	host, err := resolver.ServiceHost(r.Context(), serviceApp, service)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	credentials := make(map[string]any, len(service.Binding.Credentials)+4)
	for key, value := range service.Binding.Credentials {
		value = strings.ReplaceAll(value, "{{.Host}}", host)
		value = strings.ReplaceAll(value, "{{.Port}}", fmt.Sprint(service.Port))
		credentials[key] = value
	}
	credentials["host"], credentials["hostname"], credentials["port"] = host, host, service.Port
	uri := strings.ReplaceAll(service.Binding.URITemplate, "{{.Host}}", host)
	uri = strings.ReplaceAll(uri, "{{.Port}}", fmt.Sprint(service.Port))
	credentials["uri"] = uri
	writeJSON(w, http.StatusCreated, bindingResponse{Credentials: credentials})
}

func (b *Broker) unbind(w http.ResponseWriter, r *http.Request) {
	request := provisionQuery{ServiceID: r.URL.Query().Get("service_id"), PlanID: r.URL.Query().Get("plan_id")}
	if request.ServiceID == "" || request.PlanID == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "service_id and plan_id query parameters are required")
		return
	}
	service, _, ok := b.servicePlan(w, request.ServiceID, request.PlanID)
	if !ok {
		return
	}
	serviceApp, err := b.capi.FindServiceApp(r.Context(), appName(service.ID, chi.URLParam(r, "instance_id")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CFError", err.Error())
		return
	}
	if serviceApp != nil {
		appGUID, err := b.capi.BindingApp(r.Context(), serviceApp.GUID, chi.URLParam(r, "binding_id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "CFError", err.Error())
			return
		}
		if appGUID == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		if err := b.capi.DeletePolicy(r.Context(), appGUID, serviceApp.GUID, service.Port); err != nil {
			writeError(w, http.StatusInternalServerError, "CFError", err.Error())
			return
		}
		if err := b.capi.ForgetBinding(r.Context(), serviceApp.GUID, chi.URLParam(r, "binding_id")); err != nil {
			writeError(w, http.StatusInternalServerError, "CFError", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (b *Broker) bindingLastOperation(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, lastOperationResponse{State: "succeeded", Description: "binding operation complete"})
}

func (b *Broker) servicePlan(w http.ResponseWriter, serviceID, planID string) (config.Service, config.Plan, bool) {
	service, ok := b.services.FindService(serviceID)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "InvalidService", "unknown service id")
		return config.Service{}, config.Plan{}, false
	}
	plan, ok := service.FindPlan(planID)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "InvalidPlan", "unknown plan id")
		return config.Service{}, config.Plan{}, false
	}
	return service, plan, true
}

func appName(serviceID, instanceID string) string { return "cfe-" + serviceID + "-" + instanceID }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, osbError{Error: code, Description: message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func basicAuth(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if username == "" && password == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok || user != username || pass != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="cf-esb"`)
				writeError(w, http.StatusUnauthorized, "Unauthorized", "valid broker credentials are required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func osbVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("X-Broker-API-Version")
		if version != "" && version > osbAPIVersion {
			writeError(w, http.StatusPreconditionFailed, "UnsupportedVersion", "requested Open Service Broker API version is not supported")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func LoadServiceConfig(path string) (*config.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return config.Load(file)
}

var _ cfclient.Client
var _ resource.App
