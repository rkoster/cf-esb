package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "garage entrypoint:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := envOrDefault("GARAGE_CONFIG_FILE", "/tmp/garage.toml")
	rpcSecret, err := requiredEnv("GARAGE_RPC_SECRET")
	if err != nil {
		return err
	}
	adminToken, err := requiredEnv("GARAGE_ADMIN_TOKEN")
	if err != nil {
		return err
	}

	if err := os.MkdirAll("/tmp/garage/meta", 0o700); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	if err := os.MkdirAll("/tmp/garage/data", 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	config, err := buildConfig(rpcSecret, adminToken)
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	if err := os.Setenv("GARAGE_CONFIG_FILE", configPath); err != nil {
		return fmt.Errorf("set config file path: %w", err)
	}

	return syscall.Exec("/garage", []string{"/garage", "server", "--single-node", "--default-bucket"}, os.Environ())
}

func buildConfig(rpcSecret, adminToken string) (string, error) {
	if rpcSecret == "" {
		return "", fmt.Errorf("GARAGE_RPC_SECRET is required")
	}
	if adminToken == "" {
		return "", fmt.Errorf("GARAGE_ADMIN_TOKEN is required")
	}
	return fmt.Sprintf(`metadata_dir = "/tmp/garage/meta"
data_dir = "/tmp/garage/data"
db_engine = "sqlite"

replication_factor = 1

rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = %s

[s3_api]
api_bind_addr = "[::]:3900"
s3_region = "us-east-1"

[admin]
api_bind_addr = "127.0.0.1:3903"
admin_token = %s
`, strconv.Quote(rpcSecret), strconv.Quote(adminToken)), nil
}

func requiredEnv(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
