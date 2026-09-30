package main

import (
	"strings"
	"testing"
)

func TestBuildConfigIncludesEscapedSecretsAndServiceSettings(t *testing.T) {
	config, err := buildConfig("rpc\"secret", "admin\ntoken")
	if err != nil {
		t.Fatalf("buildConfig() error = %v", err)
	}
	for _, want := range []string{
		`metadata_dir = "/tmp/garage/meta"`,
		`data_dir = "/tmp/garage/data"`,
		`rpc_secret = "rpc\"secret"`,
		`api_bind_addr = "[::]:3900"`,
		`s3_region = "us-east-1"`,
		`admin_token = "admin\ntoken"`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config does not contain %q", want)
		}
	}
}

func TestBuildConfigRequiresSecrets(t *testing.T) {
	if _, err := buildConfig("", "admin"); err == nil {
		t.Fatal("buildConfig() accepted an empty RPC secret")
	}
	if _, err := buildConfig("rpc", ""); err == nil {
		t.Fatal("buildConfig() accepted an empty admin token")
	}
}
