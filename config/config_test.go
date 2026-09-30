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
	if garage.Image != "ghcr.io/rkoster/cf-esb-garage:2.3.1" || garage.Port != 3900 || !garage.DirectInternalDNS {
		t.Fatalf("unexpected Garage runtime configuration: %#v", garage)
	}
	if garage.Environment["GARAGE_CONFIG_FILE"] != "/etc/garage.toml" {
		t.Fatalf("Garage config path = %q, want /etc/garage.toml", garage.Environment["GARAGE_CONFIG_FILE"])
	}
	if garage.Environment["GARAGE_DEFAULT_ACCESS_KEY"] != garage.Binding.Credentials["access_key_id"] {
		t.Fatal("Garage container access key and binding access key do not match")
	}
	if garage.Environment["GARAGE_DEFAULT_SECRET_KEY"] != garage.Binding.Credentials["secret_access_key"] {
		t.Fatal("Garage container secret and binding secret do not match")
	}
	if garage.Binding.Credentials["region"] != "us-east-1" {
		t.Fatalf("Garage binding region = %q, want us-east-1", garage.Binding.Credentials["region"])
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
