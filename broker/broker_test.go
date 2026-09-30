package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudfoundry-community/cf-esb/config"
	"github.com/cloudfoundry/go-cfclient/v3/resource"
)

type fakeCAPI struct {
	apps              map[string]*resource.App
	created           int
	started           int
	deleted           int
	deleteJob         string
	deleteState       string
	deleteDescription string
	policies          map[string]bool
	bindings          map[string]string
	lastEnv           map[string]string
	lastImage         string
	lastApp           string
}

func newFakeCAPI() *fakeCAPI {
	return &fakeCAPI{apps: map[string]*resource.App{}, policies: map[string]bool{}, bindings: map[string]string{}}
}

func (f *fakeCAPI) FindServiceApp(_ context.Context, name string) (*resource.App, error) {
	return f.apps[name], nil
}
func (f *fakeCAPI) CreateServiceApp(_ context.Context, name, space string, service config.Service) (*resource.App, error) {
	f.created++
	f.lastEnv, f.lastImage, f.lastApp = service.Environment, service.Image, name
	app := &resource.App{Name: name, State: "STOPPED", Resource: resource.Resource{GUID: "service-app-guid"}, Relationships: resource.AppRelationships{Space: resource.ToOneRelationship{Data: &resource.Relationship{GUID: space}}}}
	f.apps[name] = app
	return app, nil
}
func (f *fakeCAPI) StartServiceApp(_ context.Context, guid string) error {
	f.started++
	for _, app := range f.apps {
		if app.GUID == guid {
			app.State = "STARTED"
		}
	}
	return nil
}
func (f *fakeCAPI) DeleteServiceApp(_ context.Context, guid string) (string, error) {
	f.deleted++
	if f.deleteJob != "" {
		return f.deleteJob, nil
	}
	f.deleteJob = "delete-job-guid"
	return f.deleteJob, nil
}
func (f *fakeCAPI) RememberServiceDeletionJob(_ context.Context, appGUID, jobGUID string) error {
	for _, app := range f.apps {
		if app.GUID == appGUID {
			if app.Metadata == nil {
				app.Metadata = &resource.Metadata{}
			}
			if app.Metadata.Annotations == nil {
				app.Metadata.Annotations = map[string]*string{}
			}
			app.Metadata.Annotations[serviceDeletionJobAnnotation] = &jobGUID
		}
	}
	f.deleteJob = jobGUID
	return nil
}
func (f *fakeCAPI) ServiceDeletionJob(app *resource.App) string {
	if app == nil || app.Metadata == nil || app.Metadata.Annotations == nil || app.Metadata.Annotations[serviceDeletionJobAnnotation] == nil {
		return ""
	}
	return *app.Metadata.Annotations[serviceDeletionJobAnnotation]
}
func (f *fakeCAPI) ServiceDeletionStatus(_ context.Context, jobGUID string) (string, string, error) {
	if jobGUID != f.deleteJob {
		return "", "", fmt.Errorf("unknown deletion job %q", jobGUID)
	}
	state := f.deleteState
	if state == "" {
		state = "in progress"
	}
	return state, f.deleteDescription, nil
}
func (f *fakeCAPI) CreatePolicy(_ context.Context, source, destination string, port int) error {
	f.policies[source+":"+destination+":"+itoa(port)] = true
	return nil
}
func (f *fakeCAPI) DeletePolicy(_ context.Context, source, destination string, port int) error {
	delete(f.policies, source+":"+destination+":"+itoa(port))
	return nil
}
func (f *fakeCAPI) DeletePoliciesForDestination(_ context.Context, destination string, _ int) error {
	for key := range f.policies {
		if strings.Contains(key, ":"+destination+":") {
			delete(f.policies, key)
		}
	}
	return nil
}
func (f *fakeCAPI) BindingApp(_ context.Context, serviceAppGUID, bindingGUID string) (string, error) {
	return f.bindings[serviceAppGUID+":"+bindingGUID], nil
}
func (f *fakeCAPI) RememberBinding(_ context.Context, serviceAppGUID, bindingGUID, clientAppGUID string) error {
	f.bindings[serviceAppGUID+":"+bindingGUID] = clientAppGUID
	return nil
}
func (f *fakeCAPI) ForgetBinding(_ context.Context, serviceAppGUID, bindingGUID string) error {
	delete(f.bindings, serviceAppGUID+":"+bindingGUID)
	return nil
}
func (f *fakeCAPI) ServiceHost(_ context.Context, app *resource.App, service config.Service) (string, error) {
	if service.DirectInternalDNS {
		return strings.ReplaceAll(service.HostnameTemplate, "{{.AppName}}", app.Name+"."+service.InternalDomain), nil
	}
	return strings.ReplaceAll(service.HostnameTemplate, "{{.RouteHost}}", app.Name+".apps.internal"), nil
}

func testBroker(t *testing.T) (*Broker, *fakeCAPI) {
	t.Helper()
	services, err := LoadServiceConfig("../config/services.yml")
	if err != nil {
		t.Fatal(err)
	}
	cf := newFakeCAPI()
	return New(services, cf, "managed-space"), cf
}

func request(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestCatalogListsConfiguredPostgresService(t *testing.T) {
	broker, _ := testBroker(t)
	response := request(t, Handler(broker, "", ""), http.MethodGet, "/v2/catalog", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var result catalog
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Services) != 2 || result.Services[0].ID != "postgres" || result.Services[1].ID != "garage" {
		t.Fatalf("catalog services = %#v", result.Services)
	}
}

func TestProvisionCreatesOneStatelessManagedServiceApp(t *testing.T) {
	broker, cf := testBroker(t)
	handler := Handler(broker, "", "")
	body := `{"service_id":"postgres","plan_id":"ephemeral","space_guid":"managed-space"}`
	response := request(t, handler, http.MethodPut, "/v2/service_instances/instance-123?accepts_incomplete=true", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if cf.created != 1 || cf.started != 1 || cf.lastApp != "cfe-postgres-instance-123" {
		t.Fatalf("unexpected CAPI actions: %#v", cf)
	}
	if cf.lastImage != "docker.io/library/postgres:17" || cf.lastEnv["POSTGRES_PASSWORD"] != "cf-esb-demo-password" {
		t.Fatalf("service image/env not taken from YAML: %#v", cf)
	}
	response = request(t, handler, http.MethodPut, "/v2/service_instances/instance-123?accepts_incomplete=true", body)
	if response.Code != http.StatusOK || cf.created != 1 {
		t.Fatalf("repeat provision status=%d created=%d", response.Code, cf.created)
	}
}

func TestBindAndUnbindUseStaticCredentialsAndPolicy(t *testing.T) {
	broker, cf := testBroker(t)
	app := &resource.App{Name: "cfe-postgres-instance-123", State: "STARTED", Resource: resource.Resource{GUID: "postgres-app-guid"}}
	cf.apps[app.Name] = app
	handler := Handler(broker, "", "")
	body := `{"service_id":"postgres","plan_id":"ephemeral","bind_resource":{"app_guid":"client-app-guid"}}`
	response := request(t, handler, http.MethodPut, "/v2/service_instances/instance-123/service_bindings/binding-123", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("bind status = %d, body = %s", response.Code, response.Body)
	}
	var binding bindingResponse
	if err := json.Unmarshal(response.Body.Bytes(), &binding); err != nil {
		t.Fatal(err)
	}
	if binding.Credentials["password"] != "cf-esb-demo-password" || binding.Credentials["host"] != "cfe-postgres-instance-123.apps.internal" {
		t.Fatalf("binding credentials = %#v", binding.Credentials)
	}
	if !cf.policies["client-app-guid:postgres-app-guid:5432"] {
		t.Fatalf("policy not installed: %#v", cf.policies)
	}
	if cf.bindings["postgres-app-guid:binding-123"] != "client-app-guid" {
		t.Fatalf("binding app was not recorded in CF metadata: %#v", cf.bindings)
	}
	response = request(t, handler, http.MethodDelete, "/v2/service_instances/instance-123/service_bindings/binding-123?service_id=postgres&plan_id=ephemeral", "")
	if response.Code != http.StatusOK {
		t.Fatalf("unbind status = %d, body = %s", response.Code, response.Body)
	}
	if cf.policies["client-app-guid:postgres-app-guid:5432"] {
		t.Fatalf("policy was not removed: %#v", cf.policies)
	}
}

func TestGarageBindUsesDirectInternalDNSAndTCPEndpoint(t *testing.T) {
	broker, cf := testBroker(t)
	app := &resource.App{Name: "cfe-garage-instance-123", State: "STARTED", Resource: resource.Resource{GUID: "garage-app-guid"}}
	cf.apps[app.Name] = app
	handler := Handler(broker, "", "")
	body := `{"service_id":"garage","plan_id":"garage-ephemeral","bind_resource":{"app_guid":"client-app-guid"}}`
	response := request(t, handler, http.MethodPut, "/v2/service_instances/instance-123/service_bindings/binding-garage", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("bind status = %d, body = %s", response.Code, response.Body)
	}
	var result bindingResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	wantEndpoint := "http://cfe-garage-instance-123.apps.internal:3900"
	if result.Credentials["endpoint"] != wantEndpoint || result.Credentials["uri"] != wantEndpoint {
		t.Fatalf("Garage endpoints = endpoint %v, uri %v; want %s", result.Credentials["endpoint"], result.Credentials["uri"], wantEndpoint)
	}
	if !cf.policies["client-app-guid:garage-app-guid:3900"] {
		t.Fatalf("Garage TCP/3900 policy was not installed: %#v", cf.policies)
	}
}

func TestDeprovisionReturnsAsyncAndLastOperationTracksDeleteJob(t *testing.T) {
	broker, cf := testBroker(t)
	app := &resource.App{Name: "cfe-garage-instance-123", State: "STARTED", Resource: resource.Resource{GUID: "garage-app-guid"}}
	cf.apps[app.Name] = app
	cf.policies["client-app-guid:garage-app-guid:3900"] = true
	handler := Handler(broker, "", "")
	path := "/v2/service_instances/instance-123"
	query := "?service_id=garage&plan_id=garage-ephemeral&accepts_incomplete=true"

	response := request(t, handler, http.MethodDelete, path+query, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("deprovision status = %d, body = %s", response.Code, response.Body)
	}
	var accepted map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted["operation"] != "delete-job-guid" {
		t.Fatalf("deprovision operation = %q, want delete-job-guid", accepted["operation"])
	}
	if cf.deleted != 1 || cf.policies["client-app-guid:garage-app-guid:3900"] {
		t.Fatalf("unexpected CAPI/policy state: deleted=%d policies=%#v", cf.deleted, cf.policies)
	}

	response = request(t, handler, http.MethodDelete, path+query, "")
	if response.Code != http.StatusAccepted || cf.deleted != 1 {
		t.Fatalf("repeated deprovision status=%d deleted=%d body=%s", response.Code, cf.deleted, response.Body)
	}
	var retry map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &retry); err != nil {
		t.Fatal(err)
	}
	if retry["operation"] != accepted["operation"] {
		t.Fatalf("retry operation = %q, want original %q", retry["operation"], accepted["operation"])
	}
	response = request(t, handler, http.MethodDelete, path+"?service_id=garage&plan_id=garage-ephemeral", "")
	if response.Code != http.StatusAccepted || cf.deleted != 1 {
		t.Fatalf("retry without accepts_incomplete status=%d deleted=%d body=%s", response.Code, cf.deleted, response.Body)
	}

	operationQuery := "?service_id=garage&plan_id=garage-ephemeral&operation=delete-job-guid"
	response = request(t, handler, http.MethodGet, path+"/last_operation"+operationQuery, "")
	if response.Code != http.StatusOK {
		t.Fatalf("last_operation status = %d, body = %s", response.Code, response.Body)
	}
	var operation lastOperationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.State != "in progress" {
		t.Fatalf("last_operation state = %q, want in progress", operation.State)
	}

	cf.deleteState, cf.deleteDescription = "failed", "app deletion failed"
	response = request(t, handler, http.MethodGet, path+"/last_operation"+operationQuery, "")
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.State != "failed" || operation.Description != "app deletion failed" {
		t.Fatalf("failed last_operation = %#v", operation)
	}

	cf.deleteState, cf.deleteDescription = "succeeded", "service app deletion completed"
	response = request(t, handler, http.MethodGet, path+"/last_operation"+operationQuery, "")
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.State != "succeeded" {
		t.Fatalf("completed last_operation state = %q, want succeeded", operation.State)
	}
}

func TestDeprovisionRequiresAcceptsIncompleteBeforeStarting(t *testing.T) {
	broker, cf := testBroker(t)
	app := &resource.App{Name: "cfe-postgres-instance-123", State: "STARTED", Resource: resource.Resource{GUID: "postgres-app-guid"}}
	cf.apps[app.Name] = app
	response := request(t, Handler(broker, "", ""), http.MethodDelete,
		"/v2/service_instances/instance-123?service_id=postgres&plan_id=ephemeral", "")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("deprovision status = %d, want %d: %s", response.Code, http.StatusUnprocessableEntity, response.Body)
	}
	if cf.deleted != 0 || len(cf.policies) != 0 {
		t.Fatalf("deprovision started without accepts_incomplete: deleted=%d policies=%#v", cf.deleted, cf.policies)
	}
}

func TestDeprovisionMissingAppIsIdempotent(t *testing.T) {
	broker, cf := testBroker(t)
	response := request(t, Handler(broker, "", ""), http.MethodDelete,
		"/v2/service_instances/instance-missing?service_id=garage&plan_id=garage-ephemeral&accepts_incomplete=true", "")
	if response.Code != http.StatusOK {
		t.Fatalf("deprovision missing app status = %d, want %d: %s", response.Code, http.StatusOK, response.Body)
	}
	if cf.deleted != 0 || len(cf.policies) != 0 {
		t.Fatalf("missing app triggered CAPI cleanup: deleted=%d policies=%#v", cf.deleted, cf.policies)
	}
}

func itoa(value int) string { return fmt.Sprintf("%d", value) }
