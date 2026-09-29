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
