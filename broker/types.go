package broker

type catalog struct {
	Services []catalogService `json:"services"`
}

type catalogService struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Bindable    bool          `json:"bindable"`
	Tags        []string      `json:"tags,omitempty"`
	Plans       []catalogPlan `json:"plans"`
}

type catalogPlan struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Free        bool   `json:"free"`
}

type provisionRequest struct {
	ServiceID        string `json:"service_id"`
	PlanID           string `json:"plan_id"`
	OrganizationGUID string `json:"organization_guid"`
	SpaceGUID        string `json:"space_guid"`
	Parameters       any    `json:"parameters"`
	Context          struct {
		OrganizationGUID string `json:"organization_guid"`
		SpaceGUID        string `json:"space_guid"`
	} `json:"context"`
}

type provisionQuery struct {
	ServiceID string
	PlanID    string
}

type bindRequest struct {
	ServiceID    string `json:"service_id"`
	PlanID       string `json:"plan_id"`
	AppGUID      string `json:"app_guid"`
	BindResource struct {
		AppGUID string `json:"app_guid"`
	} `json:"bind_resource"`
	Parameters any `json:"parameters"`
}

type unbindRequest struct {
	ServiceID    string `json:"service_id"`
	PlanID       string `json:"plan_id"`
	BindResource struct {
		AppGUID string `json:"app_guid"`
	} `json:"bind_resource"`
}

type bindingResponse struct {
	Credentials map[string]any `json:"credentials"`
}

type lastOperationResponse struct {
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
}

type osbError struct {
	Error       string `json:"error"`
	Description string `json:"description"`
}
