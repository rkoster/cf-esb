package config

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type File struct {
	Services []Service `yaml:"services"`
}

type Service struct {
	ID                string            `yaml:"id"`
	Name              string            `yaml:"name"`
	Description       string            `yaml:"description"`
	Bindable          bool              `yaml:"bindable"`
	Tags              []string          `yaml:"tags"`
	Image             string            `yaml:"image"`
	Port              int               `yaml:"port"`
	HostnameTemplate  string            `yaml:"hostname_template"`
	InternalDomain    string            `yaml:"internal_domain"`
	DirectInternalDNS bool              `yaml:"direct_internal_dns"`
	MemoryMB          int               `yaml:"memory_mb"`
	DiskMB            int               `yaml:"disk_mb"`
	Environment       map[string]string `yaml:"environment"`
	Command           string            `yaml:"command"`
	Plans             []Plan            `yaml:"plans"`
	Binding           Binding           `yaml:"binding"`
}

type Plan struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Free        bool   `yaml:"free"`
}

type Binding struct {
	Credentials map[string]string `yaml:"credentials"`
	URITemplate string            `yaml:"uri_template"`
}

func Load(r io.Reader) (*File, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var file File
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode service config: %w", err)
	}
	if len(file.Services) == 0 {
		return nil, fmt.Errorf("service config must define at least one service")
	}
	serviceIDs := map[string]bool{}
	for _, service := range file.Services {
		if service.ID == "" || service.Name == "" || service.Image == "" || service.Port < 1 || service.Port > 65535 {
			return nil, fmt.Errorf("service id, name, image, and valid port are required")
		}
		if service.DirectInternalDNS && strings.TrimSpace(service.InternalDomain) == "" {
			return nil, fmt.Errorf("service %q requires internal_domain when direct_internal_dns is enabled", service.ID)
		}
		if serviceIDs[service.ID] {
			return nil, fmt.Errorf("duplicate service id %q", service.ID)
		}
		serviceIDs[service.ID] = true
		if len(service.Plans) == 0 {
			return nil, fmt.Errorf("service %q must define at least one plan", service.ID)
		}
		if len(service.Binding.Credentials) == 0 || strings.TrimSpace(service.Binding.URITemplate) == "" {
			return nil, fmt.Errorf("service %q must define static binding credentials and uri_template", service.ID)
		}
		planIDs := map[string]bool{}
		for _, plan := range service.Plans {
			if plan.ID == "" || plan.Name == "" || planIDs[plan.ID] {
				return nil, fmt.Errorf("service %q has missing or duplicate plan id", service.ID)
			}
			planIDs[plan.ID] = true
		}
	}
	return &file, nil
}

func (f *File) FindService(id string) (Service, bool) {
	for _, service := range f.Services {
		if service.ID == id {
			return service, true
		}
	}
	return Service{}, false
}

func (s Service) FindPlan(id string) (Plan, bool) {
	for _, plan := range s.Plans {
		if plan.ID == id {
			return plan, true
		}
	}
	return Plan{}, false
}
