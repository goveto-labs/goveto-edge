package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	securitypolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

type securityPolicyResponse struct {
	WAF          securitypolicy.WAFPolicy       `json:"waf"`
	Access       securitypolicy.AccessPolicy    `json:"access"`
	RateLimit    securitypolicy.RateLimitPolicy `json:"rate_limit"`
	PublishJob   *types.PublishJob              `json:"publish_job,omitempty"`
	PublishError string                         `json:"publish_error,omitempty"`
}

// @summary Get site security policy
// @description Get WAF, access-control, and rate-limit policy for a site.
// @Tags sites
func getSecurity(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		result := securityPolicyResponse{
			WAF:       securitypolicy.DefaultWAFPolicy(),
			Access:    securitypolicy.DefaultAccessPolicy(),
			RateLimit: securitypolicy.DefaultRateLimitPolicy(),
		}
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
			if findErr != nil {
				return findErr
			}
			if err = json.Unmarshal(stored.WafJson, &result.WAF); err != nil {
				return err
			}
			if err = json.Unmarshal(stored.CcJson, &result.RateLimit); err != nil {
				return err
			}
			if err = json.Unmarshal(stored.AccessJson, &result.Access); err != nil {
				return err
			}
		}
		if err = result.WAF.NormalizeAndValidate(); err != nil {
			return err
		}
		if err = result.RateLimit.NormalizeAndValidate(); err != nil {
			return err
		}
		if err = result.Access.NormalizeAndValidatePublic(); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Update site security policy
// @description Update WAF, access-control, and rate-limit rules and enqueue a site publish.
// @Tags sites
func updateSecurity(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		input := securityPolicyResponse{}
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if err := input.WAF.NormalizeAndValidate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid WAF policy: "+err.Error())
		}
		if err := input.RateLimit.NormalizeAndValidate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid rate-limit policy: "+err.Error())
		}
		if err := input.Access.NormalizeAndValidatePublic(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid access policy: "+err.Error())
		}
		if err := ensureRateLimitBackendCapacity(c.Request().Context(), db, c.Param("cluster_id"), input.RateLimit); err != nil {
			return err
		}

		wafJSON, err := json.Marshal(input.WAF)
		if err != nil {
			return err
		}
		rateLimitJSON, err := json.Marshal(input.RateLimit)
		if err != nil {
			return err
		}
		accessJSON, err := json.Marshal(input.Access)
		if err != nil {
			return err
		}

		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		before := securityPolicyResponse{
			WAF: securitypolicy.DefaultWAFPolicy(), Access: securitypolicy.DefaultAccessPolicy(),
			RateLimit: securitypolicy.DefaultRateLimitPolicy(),
		}
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
			if findErr != nil {
				return findErr
			}
			if err = json.Unmarshal(stored.WafJson, &before.WAF); err != nil {
				return err
			}
			if err = json.Unmarshal(stored.CcJson, &before.RateLimit); err != nil {
				return err
			}
			if err = json.Unmarshal(stored.AccessJson, &before.Access); err != nil {
				return err
			}
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if site.PolicyId != nil {
				_, updateErr := tx.Policy.Update().
					Where(query.Policy.Id.Equals(*site.PolicyId)).
					Set(
						query.Policy.WafJson.Set(wafJSON),
						query.Policy.CcJson.Set(rateLimitJSON),
						query.Policy.AccessJson.Set(accessJSON),
					).
					Do(ctx)
				return updateErr
			}

			policyID := uuid.NewString()
			empty := json.RawMessage(`{}`)
			if _, createErr := tx.Policy.Create().
				Set(
					query.Policy.Id.Set(policyID),
					query.Policy.Name.Set("site:"+site.Id),
					query.Policy.CacheJson.Set(empty),
					query.Policy.CompressionJson.Set(empty),
					query.Policy.DeliveryJson.Set(empty),
					query.Policy.WafJson.Set(wafJSON),
					query.Policy.CcJson.Set(rateLimitJSON),
					query.Policy.AccessJson.Set(accessJSON),
				).
				Do(ctx); createErr != nil {
				return createErr
			}
			_, updateErr := tx.Site.Update().
				Where(query.Site.Id.Equals(site.Id)).
				Set(query.Site.PolicyId.Set(policyID)).
				Do(ctx)
			return updateErr
		})
		if err != nil {
			return err
		}

		response := securityPolicyResponse{WAF: input.WAF, Access: input.Access, RateLimit: input.RateLimit}
		if job, publishErr := publishService.Enqueue(ctx, site.Id); publishErr == nil {
			value := types.NewPublishJob(job)
			response.PublishJob = &value
		} else {
			response.PublishError = publishErr.Error()
		}
		audit.SetChange(c, before, response)
		return types.JSON(c, http.StatusOK, response)
	}
}

// redisCapabilityFreshness is how recently an ONLINE node must have
// heartbeated for its redis_available=false report to block enabling the
// REDIS rate-limit backend. Stale offline/failed nodes must not lock the
// cluster configuration forever.
const redisCapabilityFreshness = 2 * time.Minute

// ensureRateLimitBackendCapacity rejects the REDIS rate-limit backend when
// currently online cluster nodes report their distributed state backend as
// unavailable, instead of letting the site silently run with the configured
// failure mode (e.g. LOCAL counters). Nodes that never reported a capability
// (NULL), are not ONLINE, or have a stale heartbeat do not block the change.
func ensureRateLimitBackendCapacity(
	ctx context.Context,
	db *client.Client,
	clusterID string,
	policy securitypolicy.RateLimitPolicy,
) error {
	if !policy.Enabled || policy.Backend != "REDIS" {
		return nil
	}
	unavailable := false
	nodes, err := db.Node.Query().
		Where(
			query.Node.ClusterId.Equals(clusterID),
			query.Node.Status.Equals(model.NodeStatusONLINE),
			query.Node.RedisAvailable.Equals(&unavailable),
		).
		OrderBy(query.Node.Name.Asc()).
		Do(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var names []string
	for index := range nodes {
		if nodeBlocksRedisRateLimit(nodes[index], now) {
			names = append(names, nodes[index].Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return echo.NewHTTPError(http.StatusConflict, fmt.Sprintf(
		"rate-limit backend REDIS is unavailable on node(s) %s: agent reports EDGE_AGENT_REDIS_URL unconfigured or unreachable",
		strings.Join(names, ", "),
	))
}

// nodeBlocksRedisRateLimit reports whether a node should prevent enabling the
// REDIS rate-limit backend right now. Only fresh ONLINE reports of
// redis_available=false block the change.
func nodeBlocksRedisRateLimit(node model.Node, now time.Time) bool {
	if node.Status != model.NodeStatusONLINE {
		return false
	}
	if node.RedisAvailable == nil || *node.RedisAvailable {
		return false
	}
	if node.HeartbeatAt == nil || !node.HeartbeatAt.After(now.Add(-redisCapabilityFreshness)) {
		return false
	}
	return true
}
