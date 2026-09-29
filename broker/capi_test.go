package broker

import (
	"context"
	"testing"

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
