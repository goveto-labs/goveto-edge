package sites

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/httpapi/types"
	securitypolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type securityPolicyResponse struct {
	WAF          securitypolicy.WAFPolicy       `json:"waf"`
	RateLimit    securitypolicy.RateLimitPolicy `json:"rate_limit"`
	PublishJob   *types.PublishJob              `json:"publish_job,omitempty"`
	PublishError string                         `json:"publish_error,omitempty"`
}

// @summary Get site security policy
// @description Get the WAF and CC rate-limit policy for a site.
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
		}
		if err = result.WAF.NormalizeAndValidate(); err != nil {
			return err
		}
		if err = result.RateLimit.NormalizeAndValidate(); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Update site security policy
// @description Update WAF and CC rate-limit rules and enqueue a site publish.
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

		wafJSON, err := json.Marshal(input.WAF)
		if err != nil {
			return err
		}
		rateLimitJSON, err := json.Marshal(input.RateLimit)
		if err != nil {
			return err
		}

		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if site.PolicyId != nil {
				_, updateErr := tx.Policy.Update().
					Where(query.Policy.Id.Equals(*site.PolicyId)).
					Set(
						query.Policy.WafJson.Set(wafJSON),
						query.Policy.CcJson.Set(rateLimitJSON),
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
					query.Policy.WafJson.Set(wafJSON),
					query.Policy.CcJson.Set(rateLimitJSON),
					query.Policy.AccessJson.Set(empty),
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

		response := securityPolicyResponse{WAF: input.WAF, RateLimit: input.RateLimit}
		if job, publishErr := publishService.Enqueue(ctx, site.Id); publishErr == nil {
			value := types.NewPublishJob(job)
			response.PublishJob = &value
		} else {
			response.PublishError = publishErr.Error()
		}
		return types.JSON(c, http.StatusOK, response)
	}
}
