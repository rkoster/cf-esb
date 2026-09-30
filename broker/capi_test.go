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

func TestServiceProcessUpdatePreservesCustomCommandWithProcessHealthCheck(t *testing.T) {
	service := config.Service{Command: "/bin/sh -c 'generate-config && exec /garage server'"}
	update := serviceProcessUpdate(service)
	if update.Command == nil || *update.Command != service.Command {
		t.Fatalf("process command = %v, want %q", update.Command, service.Command)
	}
	if update.HealthCheck == nil || update.HealthCheck.Type != "process" {
		t.Fatalf("process health check = %#v, want process", update.HealthCheck)
	}
}

func TestServiceProcessUpdateKeepsImageDefaultWhenCommandIsEmpty(t *testing.T) {
	update := serviceProcessUpdate(config.Service{})
	if update.Command != nil {
		t.Fatalf("process command = %q, want nil to preserve image default", *update.Command)
	}
	if update.HealthCheck == nil || update.HealthCheck.Type != "process" {
		t.Fatalf("process health check = %#v, want process", update.HealthCheck)
	}
}

func TestServiceDeletionJobStatus(t *testing.T) {
	tests := []struct {
		name            string
		job             *resource.Job
		wantState       string
		wantDescription string
	}{
		{
			name:      "processing",
			job:       &resource.Job{State: resource.JobStateProcessing},
			wantState: "in progress", wantDescription: "waiting for Cloud Foundry to delete service app",
		},
		{
			name:      "complete",
			job:       &resource.Job{State: resource.JobStateComplete},
			wantState: "succeeded", wantDescription: "service app deletion completed",
		},
		{
			name:      "failed includes CAPI reason",
			job:       &resource.Job{State: resource.JobStateFailed, Errors: []resource.CloudFoundryError{{Detail: "app deletion failed"}}},
			wantState: "failed", wantDescription: "app deletion failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, description := serviceDeletionJobStatus(test.job)
			if state != test.wantState || description != test.wantDescription {
				t.Fatalf("status = (%q, %q), want (%q, %q)", state, description, test.wantState, test.wantDescription)
			}
		})
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

func TestInternalDiscoveryRouteIsPortlessAndMapsApp(t *testing.T) {
	route, destination := internalDiscoveryRoute("cfe-service-instance-guid", "space-guid", "domain-guid", "app-guid")
	if route.Port != nil {
		t.Fatalf("service-discovery route port = %d, want nil", *route.Port)
	}
	if destination.Port != nil || destination.Protocol != nil {
		t.Fatalf("DNS-only destination should omit port and protocol: %#v", destination)
	}
	if destination.App.GUID == nil || *destination.App.GUID != "app-guid" {
		t.Fatalf("route destination app GUID = %v", destination.App.GUID)
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
