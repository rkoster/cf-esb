package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadServiceConfig(t *testing.T) {
	input := `services:
  - id: postgres
    name: PostgreSQL
    description: test
    bindable: true
    image: postgres:17
    port: 5432
    memory_mb: 512
    disk_mb: 1024
    environment:
      POSTGRES_PASSWORD: static-password
    plans:
      - id: ephemeral
        name: ephemeral
        free: true
    binding:
      credentials:
        password: static-password
      uri_template: postgres://{{.Host}}:{{.Port}}
`
	file, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	service, ok := file.FindService("postgres")
	if !ok {
		t.Fatal("postgres service missing")
	}
	if got := service.Environment["POSTGRES_PASSWORD"]; got != "static-password" {
		t.Fatalf("password env = %q", got)
	}
	if got := service.Binding.Credentials["password"]; got != "static-password" {
		t.Fatalf("binding password = %q", got)
	}
}

func TestLoadGarageServiceConfiguration(t *testing.T) {
	file, err := os.Open("services.yml")
	if err != nil {
		t.Fatalf("open services.yml: %v", err)
	}
	defer file.Close()
	services, err := Load(file)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	garage, ok := services.FindService("garage")
	if !ok {
		t.Fatal("garage service missing")
	}
	if garage.Image != "docker.io/dxflrs/garage:v2.3.0" || garage.Port != 3900 || !garage.DirectInternalDNS {
		t.Fatalf("unexpected Garage runtime configuration: %#v", garage)
	}
	if garage.Environment["GARAGE_DEFAULT_ACCESS_KEY"] != garage.Binding.Credentials["access_key_id"] {
		t.Fatal("Garage container access key and binding access key do not match")
	}
	if garage.Environment["GARAGE_DEFAULT_SECRET_KEY"] != garage.Binding.Credentials["secret_access_key"] {
		t.Fatal("Garage container secret and binding secret do not match")
	}
	if garage.Binding.Credentials["region"] != "us-east-1" || !strings.Contains(garage.Command, `s3_region = \"us-east-1\"`) {
		t.Fatalf("Garage server and binding region must both be us-east-1: binding=%q command=%q", garage.Binding.Credentials["region"], garage.Command)
	}
}

func TestLoadRejectsMissingBindingConfiguration(t *testing.T) {
	input := `services:
  - id: postgres
    name: PostgreSQL
    image: postgres:17
    port: 5432
    plans:
      - id: ephemeral
        name: ephemeral
`
	if _, err := Load(strings.NewReader(input)); err == nil {
		t.Fatal("Load() accepted service without static binding config")
	}
}
