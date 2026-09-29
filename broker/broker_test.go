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
	apps      map[string]*resource.App
	created   int
	started   int
	deleted   int
	policies  map[string]bool
	bindings  map[string]string
	lastEnv   map[string]string
	lastImage string
	lastApp   string
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
func (f *fakeCAPI) DeleteServiceApp(_ context.Context, guid string) error {
	f.deleted++
	for name, app := range f.apps {
		if app.GUID == guid {
			delete(f.apps, name)
		}
	}
	return nil
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

func itoa(value int) string { return fmt.Sprintf("%d", value) }
