package identity

type OrganizationState string

const (
	OrganizationCreating OrganizationState = "creating"
	OrganizationActive   OrganizationState = "active"
	OrganizationDisabled OrganizationState = "disabled"
	OrganizationDeleting OrganizationState = "deleting"
	Creating                               = OrganizationCreating
	Active                                 = OrganizationActive
	Disabled                               = OrganizationDisabled
	Deleting                               = OrganizationDeleting
)

type Organization struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	State     OrganizationState `json:"state"`
	Version   int64             `json:"version"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}
