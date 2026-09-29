package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudfoundry-community/cf-esb/broker"
)

func main() {
	if os.Getenv("BROKER_USERNAME") == "" || os.Getenv("BROKER_PASSWORD") == "" {
		log.Fatal("BROKER_USERNAME and BROKER_PASSWORD are required")
	}
	services, err := broker.LoadServiceConfig(envOr("SERVICES_CONFIG", "config/services.yml"))
	if err != nil {
		log.Fatalf("load services config: %v", err)
	}
	cf, err := broker.NewCloudFoundryFromEnv()
	if err != nil {
		log.Fatalf("configure Cloud Foundry client: %v", err)
	}
	spaceGUID, err := cf.ResolveSpace(context.Background(), os.Getenv("CF_ORG"), os.Getenv("CF_INSTANCE_SPACE"))
	if err != nil {
		log.Fatalf("resolve service instances space: %v", err)
	}
	cf.SetManagedSpace(spaceGUID)
	port := envOr("PORT", "8080")
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           broker.Handler(broker.New(services, cf, spaceGUID), os.Getenv("BROKER_USERNAME"), os.Getenv("BROKER_PASSWORD")),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("starting CF-ESB on :%s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("broker server: %v", err)
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
