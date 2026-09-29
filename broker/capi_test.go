package broker

import (
	"context"
	"testing"

	"github.com/cloudfoundry-community/cf-esb/config"
	"github.com/cloudfoundry/go-cfclient/v3/resource"
)

type fakeDropletAssigner struct {
	appGUID     string
	dropletGUID string
	called      bool
}

func (f *fakeDropletAssigner) SetCurrentAssociationForApp(_ context.Context, appGUID, dropletGUID string) (*resource.DropletCurrent, error) {
	f.appGUID, f.dropletGUID, f.called = appGUID, dropletGUID, true
	return &resource.DropletCurrent{}, nil
}

func TestSetCurrentDropletAssignsStagedDroplet(t *testing.T) {
	assigner := &fakeDropletAssigner{}
	if err := setCurrentDroplet(context.Background(), assigner, "app-guid", "droplet-guid"); err != nil {
		t.Fatalf("setCurrentDroplet() error = %v", err)
	}
	if !assigner.called || assigner.appGUID != "app-guid" || assigner.dropletGUID != "droplet-guid" {
		t.Fatalf("unexpected assignment: %#v", assigner)
	}
}

func TestSetCurrentDropletRejectsEmptyDroplet(t *testing.T) {
	assigner := &fakeDropletAssigner{}
	if err := setCurrentDroplet(context.Background(), assigner, "app-guid", ""); err == nil {
		t.Fatal("setCurrentDroplet() accepted an empty droplet GUID")
	}
	if assigner.called {
		t.Fatal("droplet assignment called for empty GUID")
	}
}

func TestGarageServiceHostUsesMappedInternalRouteDNSName(t *testing.T) {
	app := &resource.App{Name: "cfe-garage-instance-guid"}
	service := config.Service{
		ID: "garage", HostnameTemplate: "{{.AppName}}", InternalDomain: "apps.internal", DirectInternalDNS: true,
	}
	host, err := directInternalServiceHost(app, service)
	if err != nil {
		t.Fatalf("directInternalServiceHost() error = %v", err)
	}
	if want := "cfe-garage-instance-guid.apps.internal"; host != want {
		t.Fatalf("host = %q, want %q", host, want)
	}
}

func TestGarageInternalDiscoveryRouteIsPortlessAndMapsApp(t *testing.T) {
	route, destination := internalDiscoveryRoute("cfe-garage-instance-guid", "space-guid", "domain-guid", "app-guid")
	if route.Port != nil {
		t.Fatalf("Garage discovery route port = %d, want nil", *route.Port)
	}
	if destination.Port != nil || destination.Protocol != nil {
		t.Fatalf("Garage DNS-only destination should omit port and protocol: %#v", destination)
	}
	if destination.App.GUID == nil || *destination.App.GUID != "app-guid" {
		t.Fatalf("route destination app GUID = %v", destination.App.GUID)
	}
}

func TestPostgresServiceRouteKeepsTCPPort(t *testing.T) {
	port := intPointer(5432)
	route, destination := serviceInternalRoute("cfe-postgres-instance-guid", "space-guid", "domain-guid", "app-guid", port)
	if route.Port == nil || *route.Port != 5432 || destination.Port == nil || *destination.Port != 5432 {
		t.Fatalf("PostgreSQL route/destination ports should remain TCP 5432: route=%v destination=%v", route.Port, destination.Port)
	}
	if destination.Protocol == nil || *destination.Protocol != "tcp" {
		t.Fatalf("PostgreSQL destination protocol = %v, want tcp", destination.Protocol)
	}
}

func TestRememberBindingUpdatePreservesAppName(t *testing.T) {
	app := &resource.App{
		Name: "cfe-garage-instance-guid",
		Metadata: &resource.Metadata{
			Labels:      map[string]*string{"owner": stringPointer("cf-esb")},
			Annotations: map[string]*string{"existing": stringPointer("value")},
		},
	}
	annotations := rememberBindingAnnotation(app.Metadata.Annotations, "binding-guid", "client-app-guid")
	update := appMetadataUpdate(app, app.Metadata.Labels, annotations)

	if update.Name != app.Name {
		t.Fatalf("RememberBinding update name = %q, want %q", update.Name, app.Name)
	}
	if got := *update.Metadata.Annotations[bindingAnnotationKey("binding-guid")]; got != "client-app-guid" {
		t.Fatalf("remembered client app GUID = %q", got)
	}
	if got := *update.Metadata.Annotations["existing"]; got != "value" {
		t.Fatalf("existing annotation changed to %q", got)
	}
}

func TestForgetBindingUpdatePreservesAppName(t *testing.T) {
	app := &resource.App{
		Name: "cfe-garage-instance-guid",
		Metadata: &resource.Metadata{
			Labels: map[string]*string{"owner": stringPointer("cf-esb")},
			Annotations: map[string]*string{
				bindingAnnotationKey("binding-guid"): stringPointer("client-app-guid"),
				"existing":                           stringPointer("value"),
			},
		},
	}
	annotations := forgetBindingAnnotation(app.Metadata.Annotations, "binding-guid")
	update := appMetadataUpdate(app, app.Metadata.Labels, annotations)

	if update.Name != app.Name {
		t.Fatalf("ForgetBinding update name = %q, want %q", update.Name, app.Name)
	}
	if _, exists := update.Metadata.Annotations[bindingAnnotationKey("binding-guid")]; exists {
		t.Fatal("ForgetBinding update retained the deleted binding annotation")
	}
	if got := *update.Metadata.Annotations["existing"]; got != "value" {
		t.Fatalf("existing annotation changed to %q", got)
	}
}
