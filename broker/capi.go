package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	serviceconfig "github.com/cloudfoundry-community/cf-esb/config"
	cfclient "github.com/cloudfoundry/go-cfclient/v3/client"
	"github.com/cloudfoundry/go-cfclient/v3/config"
	"github.com/cloudfoundry/go-cfclient/v3/resource"
)

type capi interface {
	FindServiceApp(context.Context, string) (*resource.App, error)
	CreateServiceApp(context.Context, string, string, serviceconfig.Service) (*resource.App, error)
	StartServiceApp(context.Context, string) error
	DeleteServiceApp(context.Context, string) error
	CreatePolicy(context.Context, string, string, int) error
	DeletePoliciesForDestination(context.Context, string, int) error
	DeletePolicy(context.Context, string, string, int) error
	BindingApp(context.Context, string, string) (string, error)
	RememberBinding(context.Context, string, string, string) error
	ForgetBinding(context.Context, string, string) error
}

type cloudFoundry struct {
	client    *cfclient.Client
	apiURL    string
	spaceGUID string
	http      *http.Client
}

func NewCloudFoundry(apiURL, uaaURL, clientID, clientSecret string) (*cloudFoundry, error) {
	if apiURL == "" {
		return nil, fmt.Errorf("CF_API_URL is required")
	}
	if uaaURL == "" {
		return nil, fmt.Errorf("CF_UAA_URL is required")
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("CF_CLIENT_ID and CF_CLIENT_SECRET are required")
	}
	loginURL := strings.TrimSpace(os.Getenv("CF_LOGIN_URL"))
	if loginURL == "" {
		return nil, fmt.Errorf("CF_LOGIN_URL is required")
	}
	options := []config.Option{config.AuthTokenURL(loginURL, uaaURL)}
	options = append(options, config.ClientCredentials(clientID, clientSecret))
	if strings.EqualFold(os.Getenv("CF_SKIP_TLS_VALIDATION"), "true") {
		options = append(options, config.SkipTLSValidation())
	}
	cfConfig, err := config.New(apiURL, options...)
	if err != nil {
		return nil, fmt.Errorf("configure Cloud Foundry client: %w", err)
	}
	client, err := cfclient.New(cfConfig)
	if err != nil {
		return nil, fmt.Errorf("create Cloud Foundry client: %w", err)
	}
	return &cloudFoundry{
		client: client, apiURL: strings.TrimRight(apiURL, "/"),
		http: cfConfig.HTTPAuthClient(),
	}, nil
}

func NewCloudFoundryFromEnv() (*cloudFoundry, error) {
	return NewCloudFoundry(os.Getenv("CF_API_URL"), os.Getenv("CF_UAA_URL"), os.Getenv("CF_CLIENT_ID"), os.Getenv("CF_CLIENT_SECRET"))
}

func (c *cloudFoundry) SetManagedSpace(guid string) { c.spaceGUID = guid }

func (c *cloudFoundry) SetInstanceSpace(guid string) { c.spaceGUID = guid }

func (c *cloudFoundry) FindServiceApp(ctx context.Context, name string) (*resource.App, error) {
	options := cfclient.NewAppListOptions()
	options.Names.EqualTo(name)
	if c.spaceGUID != "" {
		options.SpaceGUIDs.EqualTo(c.spaceGUID)
	}
	apps, err := c.client.Applications.ListAll(ctx, options)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}
	if len(apps) > 1 {
		return nil, fmt.Errorf("multiple managed apps named %q found", name)
	}
	return apps[0], nil
}

func (c *cloudFoundry) CreateServiceApp(ctx context.Context, name, spaceGUID string, service serviceconfig.Service) (*resource.App, error) {
	app, err := c.client.Applications.Create(ctx, &resource.AppCreate{
		Name:                 name,
		Relationships:        resource.SpaceRelationship{Space: resource.ToOneRelationship{Data: &resource.Relationship{GUID: spaceGUID}}},
		EnvironmentVariables: service.Environment,
		Lifecycle:            &resource.Lifecycle{Type: "docker", Data: resource.DockerLifecycle{}},
	})
	if err != nil {
		return nil, err
	}
	dockerPackage, err := c.client.Packages.Create(ctx, resource.NewDockerPackageCreate(app.GUID, service.Image, "", ""))
	if err != nil {
		return nil, fmt.Errorf("create Docker package for %s: %w", name, err)
	}
	if err := waitForPackageReady(ctx, c.client, dockerPackage.GUID); err != nil {
		return nil, err
	}
	build, err := c.client.Builds.Create(ctx, resource.NewBuildCreate(dockerPackage.GUID))
	if err != nil {
		return nil, fmt.Errorf("create Docker build for %s: %w", name, err)
	}
	dropletGUID, err := waitForBuildStaged(ctx, c.client, build.GUID)
	if err != nil {
		return nil, err
	}
	if err := setCurrentDroplet(ctx, c.client.Droplets, app.GUID, dropletGUID); err != nil {
		return nil, fmt.Errorf("assign staged droplet to %s: %w", name, err)
	}
	// The process health check must not probe a routable port: PostgreSQL is an
	// internal TCP service with no CF route. Setting it to process lets CF track
	// the long-running container without requiring an app route.
	process, err := c.client.Processes.FirstForApp(ctx, app.GUID, nil)
	if err != nil {
		return nil, err
	}
	var command *string
	if service.Command != "" {
		command = &service.Command
	}
	if command != nil {
		if _, err := c.client.Processes.Update(ctx, process.GUID, &resource.ProcessUpdate{Command: command}); err != nil {
			return nil, fmt.Errorf("configure command for %s: %w", name, err)
		}
	}
	_, err = c.client.Processes.Update(ctx, process.GUID, &resource.ProcessUpdate{
		HealthCheck: &resource.ProcessHealthCheck{Type: "process", Data: resource.ProcessHealthCheckData{}},
	})
	if err != nil {
		return nil, err
	}
	if service.MemoryMB > 0 || service.DiskMB > 0 {
		scale := &resource.ProcessScale{}
		if service.MemoryMB > 0 {
			scale.MemoryInMB = intPointer(service.MemoryMB)
		}
		if service.DiskMB > 0 {
			scale.DiskInMB = intPointer(service.DiskMB)
		}
		if _, err := c.client.Processes.Scale(ctx, process.GUID, scale); err != nil {
			return nil, err
		}
	}
	return app, nil
}

type currentDropletAssigner interface {
	SetCurrentAssociationForApp(context.Context, string, string) (*resource.DropletCurrent, error)
}

func setCurrentDroplet(ctx context.Context, assigner currentDropletAssigner, appGUID, dropletGUID string) error {
	if dropletGUID == "" {
		return fmt.Errorf("cannot start app %s without a staged droplet", appGUID)
	}
	_, err := assigner.SetCurrentAssociationForApp(ctx, appGUID, dropletGUID)
	return err
}

func waitForPackageReady(ctx context.Context, cf *cfclient.Client, guid string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pkg, err := cf.Packages.Get(ctx, guid)
		if err != nil {
			return err
		}
		switch pkg.State {
		case resource.PackageStateReady:
			return nil
		case resource.PackageStateFailed:
			return fmt.Errorf("Docker package %s failed to become ready", guid)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForBuildStaged(ctx context.Context, cf *cfclient.Client, guid string) (string, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		build, err := cf.Builds.Get(ctx, guid)
		if err != nil {
			return "", err
		}
		switch build.State {
		case resource.BuildStateStaged:
			if build.Droplet == nil || build.Droplet.GUID == "" {
				return "", fmt.Errorf("Docker build %s staged without a droplet", guid)
			}
			return build.Droplet.GUID, nil
		case resource.BuildStateFailed:
			if build.Error != nil {
				return "", fmt.Errorf("Docker build failed: %s", *build.Error)
			}
			return "", fmt.Errorf("Docker build %s failed", guid)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *cloudFoundry) ServiceHost(ctx context.Context, app *resource.App, service serviceconfig.Service) (string, error) {
	if service.HostnameTemplate == "" {
		return "", fmt.Errorf("service %q has no hostname_template", service.ID)
	}
	if service.DirectInternalDNS {
		if _, err := c.ensureInternalDiscoveryRoute(ctx, app, service); err != nil {
			return "", err
		}
		return directInternalServiceHost(app, service)
	}
	if !strings.Contains(service.HostnameTemplate, "{{.RouteHost}}") {
		return service.HostnameTemplate, nil
	}
	return c.ensureRouteBackedServiceHost(ctx, app, service)
}

func (c *cloudFoundry) ensureRouteBackedServiceHost(ctx context.Context, app *resource.App, service serviceconfig.Service) (string, error) {
	if service.InternalDomain == "" {
		return "", fmt.Errorf("service %q requires internal_domain for app service discovery", service.ID)
	}
	// PostgreSQL uses its TCP listener port on its internal service route.
	options := cfclient.NewRouteListOptions()
	options.AppGUIDs.EqualTo(app.GUID)
	routes, err := c.client.Routes.ListAll(ctx, options)
	if err != nil {
		return "", err
	}
	domains, err := c.client.Domains.ListAll(ctx, nil)
	if err != nil {
		return "", err
	}
	for _, route := range routes {
		if route.Host != app.Name || route.Relationships.Space.Data == nil || route.Relationships.Space.Data.GUID != c.spaceGUID || route.Port == nil || route.Relationships.Domain.Data == nil {
			continue
		}
		for _, domain := range domains {
			if domain.GUID == route.Relationships.Domain.Data.GUID && domain.Name == service.InternalDomain && domain.Internal {
				return route.Host + "." + domain.Name, nil
			}
		}
	}

	var internalDomain *resource.Domain
	for _, domain := range domains {
		if domain.Name == service.InternalDomain && domain.Internal {
			internalDomain = domain
			break
		}
	}
	if internalDomain == nil {
		return "", fmt.Errorf("CF internal domain %q was not found", service.InternalDomain)
	}
	host := app.Name
	port := intPointer(service.Port)
	createRoute, destination := serviceInternalRoute(host, c.spaceGUID, internalDomain.GUID, app.GUID, port)
	createdRoute, err := c.client.Routes.Create(ctx, createRoute)
	if err != nil {
		return "", err
	}
	_, err = c.client.Routes.InsertDestinations(ctx, createdRoute.GUID, []*resource.RouteDestinationInsertOrReplace{destination})
	if err != nil {
		return "", err
	}
	return host + "." + service.InternalDomain, nil
}

func (c *cloudFoundry) ensureInternalDiscoveryRoute(ctx context.Context, app *resource.App, service serviceconfig.Service) (string, error) {
	if service.InternalDomain == "" {
		return "", fmt.Errorf("service %q requires internal_domain for app service discovery", service.ID)
	}
	options := cfclient.NewRouteListOptions()
	options.AppGUIDs.EqualTo(app.GUID)
	routes, err := c.client.Routes.ListAll(ctx, options)
	if err != nil {
		return "", err
	}
	domains, err := c.client.Domains.ListAll(ctx, nil)
	if err != nil {
		return "", err
	}
	for _, route := range routes {
		if route.Host != app.Name || route.Relationships.Space.Data == nil || route.Relationships.Space.Data.GUID != c.spaceGUID || route.Port != nil || route.Relationships.Domain.Data == nil {
			continue
		}
		for _, domain := range domains {
			if domain.GUID == route.Relationships.Domain.Data.GUID && domain.Name == service.InternalDomain && domain.Internal {
				return route.Host + "." + domain.Name, nil
			}
		}
	}
	var internalDomain *resource.Domain
	for _, domain := range domains {
		if domain.Name == service.InternalDomain && domain.Internal {
			internalDomain = domain
			break
		}
	}
	if internalDomain == nil {
		return "", fmt.Errorf("CF internal domain %q was not found", service.InternalDomain)
	}
	host := app.Name
	createRoute, destination := internalDiscoveryRoute(host, c.spaceGUID, internalDomain.GUID, app.GUID)
	createdRoute, err := c.client.Routes.Create(ctx, createRoute)
	if err != nil {
		return "", err
	}
	if _, err = c.client.Routes.InsertDestinations(ctx, createdRoute.GUID, []*resource.RouteDestinationInsertOrReplace{destination}); err != nil {
		return "", err
	}
	return host + "." + service.InternalDomain, nil
}

func internalDiscoveryRoute(host, spaceGUID, domainGUID, appGUID string) (*resource.RouteCreate, *resource.RouteDestinationInsertOrReplace) {
	return &resource.RouteCreate{
			Host: &host,
			Port: nil,
			Relationships: resource.RouteRelationships{
				Space:  resource.ToOneRelationship{Data: &resource.Relationship{GUID: spaceGUID}},
				Domain: resource.ToOneRelationship{Data: &resource.Relationship{GUID: domainGUID}},
			},
		}, &resource.RouteDestinationInsertOrReplace{
			App: resource.RouteDestinationApp{GUID: &appGUID},
		}
}

func serviceInternalRoute(host, spaceGUID, domainGUID, appGUID string, port *int) (*resource.RouteCreate, *resource.RouteDestinationInsertOrReplace) {
	protocol := "tcp"
	var protocolPointer *string
	if port != nil {
		protocolPointer = &protocol
	}
	return &resource.RouteCreate{
		Host: &host,
		Port: port,
		Relationships: resource.RouteRelationships{
			Space:  resource.ToOneRelationship{Data: &resource.Relationship{GUID: spaceGUID}},
			Domain: resource.ToOneRelationship{Data: &resource.Relationship{GUID: domainGUID}},
		},
	}, &resource.RouteDestinationInsertOrReplace{App: resource.RouteDestinationApp{GUID: &appGUID}, Port: port, Protocol: protocolPointer}
}

func directInternalServiceHost(app *resource.App, service serviceconfig.Service) (string, error) {
	if service.InternalDomain == "" {
		return "", fmt.Errorf("service %q has direct_internal_dns enabled without internal_domain", service.ID)
	}
	// CF app-service-discovery resolves the mapped apps.internal route through
	// BOSH DNS/SDC to the app's Silk overlay IP. The route is a DNS registration;
	// the TCP app-to-app policy controls direct traffic to the configured port.
	// Garage data requests do not go through Gorouter.
	return strings.ReplaceAll(service.HostnameTemplate, "{{.AppName}}", app.Name+"."+service.InternalDomain), nil
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }

func (c *cloudFoundry) StartServiceApp(ctx context.Context, guid string) error {
	_, err := c.client.Applications.Start(ctx, guid)
	return err
}

func (c *cloudFoundry) DeleteServiceApp(ctx context.Context, guid string) error {
	_, err := c.client.Applications.Delete(ctx, guid)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		app, err := c.client.Applications.Get(ctx, guid)
		if err != nil {
			if resource.IsNotFoundError(err) {
				return nil
			}
			return err
		}
		if app == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *cloudFoundry) ResolveSpace(ctx context.Context, orgName, spaceName string) (string, error) {
	if spaceGUID := strings.TrimSpace(os.Getenv("CF_INSTANCE_SPACE_GUID")); spaceGUID != "" {
		return spaceGUID, nil
	}
	if spaceName == "" {
		return "", fmt.Errorf("CF_INSTANCE_SPACE is required")
	}
	options := cfclient.NewSpaceListOptions()
	options.Names.EqualTo(spaceName)
	spaces, err := c.client.Spaces.ListAll(ctx, options)
	if err != nil {
		return "", err
	}
	matches := make([]string, 0, 1)
	for _, space := range spaces {
		if space.Name != spaceName {
			continue
		}
		if orgName != "" {
			if space.Relationships.Organization.Data == nil {
				continue
			}
			org, err := c.client.Organizations.Get(ctx, space.Relationships.Organization.Data.GUID)
			if err != nil {
				return "", err
			}
			if org.Name != orgName {
				continue
			}
		}
		matches = append(matches, space.GUID)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one matching CF space, found %d", len(matches))
	}
	return matches[0], nil
}

func (c *cloudFoundry) CreatePolicy(ctx context.Context, sourceGUID, destinationGUID string, port int) error {
	policies, err := c.listPolicies(ctx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if policy.Source.ID == sourceGUID && policy.Destination.ID == destinationGUID && policy.Destination.Protocol == "tcp" && policy.Destination.Ports.Start == port && policy.Destination.Ports.End == port {
			return nil
		}
	}
	policy := map[string]any{"source": map[string]string{"id": sourceGUID}, "destination": map[string]any{"id": destinationGUID, "protocol": "tcp", "ports": map[string]int{"start": port, "end": port}}}
	return c.policyRequest(ctx, http.MethodPost, "/networking/v1/external/policies", map[string]any{"policies": []any{policy}}, false)
}

func (c *cloudFoundry) DeletePoliciesForDestination(ctx context.Context, destinationGUID string, port int) error {
	policies, err := c.listPolicies(ctx)
	if err != nil {
		return err
	}
	matching := make([]any, 0)
	for _, policy := range policies {
		if policy.Destination.ID == destinationGUID {
			matching = append(matching, policyPayload(policy))
		}
	}
	if len(matching) == 0 {
		return nil
	}
	return c.policyRequest(ctx, http.MethodPost, "/networking/v1/external/policies/delete", map[string]any{"policies": matching}, true)
}

func (c *cloudFoundry) DeletePolicy(ctx context.Context, sourceGUID, destinationGUID string, port int) error {
	policies, err := c.listPolicies(ctx)
	if err != nil {
		return err
	}
	matching := make([]any, 0)
	for _, policy := range policies {
		if policy.Source.ID == sourceGUID && policy.Destination.ID == destinationGUID {
			matching = append(matching, policyPayload(policy))
		}
	}
	if len(matching) == 0 {
		return nil
	}
	return c.policyRequest(ctx, http.MethodPost, "/networking/v1/external/policies/delete", map[string]any{"policies": matching}, true)
}

func bindingAnnotationKey(bindingGUID string) string { return "cf-esb.binding." + bindingGUID }

func (c *cloudFoundry) BindingApp(ctx context.Context, serviceAppGUID, bindingGUID string) (string, error) {
	app, err := c.client.Applications.Get(ctx, serviceAppGUID)
	if err != nil {
		if resource.IsNotFoundError(err) {
			return "", nil
		}
		return "", err
	}
	if app.Metadata == nil {
		return "", nil
	}
	value := app.Metadata.Annotations[bindingAnnotationKey(bindingGUID)]
	if value == nil {
		return "", nil
	}
	return *value, nil
}

func (c *cloudFoundry) RememberBinding(ctx context.Context, serviceAppGUID, bindingGUID, clientAppGUID string) error {
	app, err := c.client.Applications.Get(ctx, serviceAppGUID)
	if err != nil {
		return err
	}
	metadata := app.Metadata
	if metadata == nil {
		metadata = &resource.Metadata{}
	}
	annotations := rememberBindingAnnotation(metadata.Annotations, bindingGUID, clientAppGUID)
	_, err = c.client.Applications.Update(ctx, serviceAppGUID, appMetadataUpdate(app, metadata.Labels, annotations))
	return err
}

func (c *cloudFoundry) ForgetBinding(ctx context.Context, serviceAppGUID, bindingGUID string) error {
	app, err := c.client.Applications.Get(ctx, serviceAppGUID)
	if err != nil {
		if resource.IsNotFoundError(err) {
			return nil
		}
		return err
	}
	if app.Metadata == nil {
		return nil
	}
	annotations := forgetBindingAnnotation(app.Metadata.Annotations, bindingGUID)
	_, err = c.client.Applications.Update(ctx, serviceAppGUID, appMetadataUpdate(app, app.Metadata.Labels, annotations))
	return err
}

func rememberBindingAnnotation(existing map[string]*string, bindingGUID, clientAppGUID string) map[string]*string {
	annotations := make(map[string]*string, len(existing)+1)
	for key, value := range existing {
		annotations[key] = value
	}
	annotations[bindingAnnotationKey(bindingGUID)] = &clientAppGUID
	return annotations
}

func forgetBindingAnnotation(existing map[string]*string, bindingGUID string) map[string]*string {
	annotations := make(map[string]*string, len(existing))
	for key, value := range existing {
		if key != bindingAnnotationKey(bindingGUID) {
			annotations[key] = value
		}
	}
	return annotations
}

func appMetadataUpdate(app *resource.App, labels, annotations map[string]*string) *resource.AppUpdate {
	return &resource.AppUpdate{
		Name:     app.Name,
		Metadata: &resource.Metadata{Labels: labels, Annotations: annotations},
	}
}

type networkPolicy struct {
	Source struct {
		ID string `json:"id"`
	} `json:"source"`
	Destination struct {
		ID       string `json:"id"`
		Protocol string `json:"protocol"`
		Ports    struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"ports"`
	} `json:"destination"`
}

func policyPayload(policy networkPolicy) map[string]any {
	return map[string]any{
		"source":      map[string]string{"id": policy.Source.ID},
		"destination": map[string]any{"id": policy.Destination.ID, "protocol": policy.Destination.Protocol, "ports": map[string]int{"start": policy.Destination.Ports.Start, "end": policy.Destination.Ports.End}},
	}
}

func (c *cloudFoundry) listPolicies(ctx context.Context) ([]networkPolicy, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+"/networking/v1/external/policies", nil)
	if err != nil {
		return nil, err
	}
	if err := c.authorize(ctx, request); err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseError(response)
	}
	var result struct {
		Policies []networkPolicy `json:"policies"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Policies, nil
}

func (c *cloudFoundry) policyRequest(ctx context.Context, method, path string, body any, allowNotFound bool) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if err := c.authorize(ctx, request); err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if allowNotFound && response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	return nil
}

func (c *cloudFoundry) authorize(ctx context.Context, request *http.Request) error {
	// c.http is go-cfclient's authenticated HTTP client. Its OAuth transport
	// applies the same configured CA/TLS policy as the CAPI v3 client.
	return nil
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("Cloud Foundry API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
}
