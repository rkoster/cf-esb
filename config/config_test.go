package config

import (
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
