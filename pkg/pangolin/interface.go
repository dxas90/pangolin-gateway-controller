package pangolin

import (
	"context"
)

// ClientInterface defines the methods used by controllers.
// This allows mocking the Pangolin client in tests.
type ClientInterface interface {
	// Site operations
	PickSiteDefaults(ctx context.Context) (*SiteDefaults, error)
	CreateSite(ctx context.Context, site *Site) (*Site, error)
	ListSites(ctx context.Context) ([]Site, error)
	DeleteSite(ctx context.Context, siteID int) error

	// Resource operations
	CreateResource(ctx context.Context, resourceData map[string]interface{}) (map[string]interface{}, error)
	ListResources(ctx context.Context) ([]map[string]interface{}, error)
	UpdateResource(ctx context.Context, resourceID string, data map[string]interface{}) error
	DeleteResource(ctx context.Context, resourceID string) error
	DisableSSO(ctx context.Context, resourceID string) error

	// Target operations
	CreateTargetRaw(ctx context.Context, resourceID string, targetData map[string]interface{}) (map[string]interface{}, error)
	ListTargetsRaw(ctx context.Context, resourceID string) ([]map[string]interface{}, error)
	DeleteTarget(ctx context.Context, targetID string) error

	// Domain operations
	ListDomains(ctx context.Context) ([]map[string]interface{}, error)

	// Version detection — queries the Pangolin server version via the newt auth
	// token endpoint (same mechanism the newt VPN client uses on startup).
	GetServerVersion(ctx context.Context, newtEndpoint, newtID, newtSecret string) (string, error)
}

// Ensure Client implements ClientInterface
var _ ClientInterface = (*Client)(nil)
