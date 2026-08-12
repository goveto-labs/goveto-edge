package sites

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	securitypolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
	"goveto-edge/internal/wafdsl"
)

type securityPolicyResponse struct {
	WAF          securitypolicy.WAFPolicy `json:"waf"`
	PublishJob   *types.PublishJob        `json:"publish_job,omitempty"`
	PublishError string                   `json:"publish_error,omitempty"`
}

type wafDSLRequest struct {
	Scope  wafdsl.Scope             `json:"scope"`
	Target wafdsl.Target            `json:"target"`
	WAF    securitypolicy.WAFPolicy `json:"waf"`
	Source string                   `json:"source,omitempty"`
}

type wafDSLRenderResponse struct {
	Source string `json:"source"`
}

// @summary Render WAF DSL
// @description Render a policy, rule set, rule, or condition group as canonical WAF DSL.
// @Tags sites
func renderSecurityDSL(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		input := wafDSLRequest{}
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		source, err := wafdsl.Render(input.WAF, input.Scope, input.Target)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return types.JSON(c, http.StatusOK, wafDSLRenderResponse{Source: source})
	}
}

// @summary Validate WAF DSL
// @description Parse a WAF DSL fragment, merge it into the supplied draft, and validate the complete policy.
// @Tags sites
func validateSecurityDSL(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		input := wafDSLRequest{}
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		return types.JSON(c, http.StatusOK, wafdsl.ParseAndApply(input.WAF, input.Scope, input.Target, input.Source))
	}
}

// @summary Get site security policy
// @description Get the ordered WAF rule sets for a site.
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
		result := securityPolicyResponse{WAF: securitypolicy.DefaultWAFPolicy()}
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
			if findErr != nil {
				return findErr
			}
			result.WAF = securitypolicy.WAFPolicy{}
			if err = json.Unmarshal(stored.WafJson, &result.WAF); err != nil {
				return err
			}
		}
		if err = result.WAF.NormalizeAndValidatePublic(); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Update site security policy
// @description Update ordered WAF rule sets and enqueue a site publish.
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
		if err := input.WAF.NormalizeAndValidatePublic(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid WAF policy: "+err.Error())
		}
		wafJSON, err := json.Marshal(input.WAF)
		if err != nil {
			return err
		}
		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		before := securityPolicyResponse{WAF: securitypolicy.DefaultWAFPolicy()}
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
			if findErr != nil {
				return findErr
			}
			before.WAF = securitypolicy.WAFPolicy{}
			if err = json.Unmarshal(stored.WafJson, &before.WAF); err != nil {
				return err
			}
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if site.PolicyId != nil {
				_, updateErr := tx.Policy.Update().Where(query.Policy.Id.Equals(*site.PolicyId)).Set(
					query.Policy.WafJson.Set(wafJSON),
				).Do(ctx)
				return updateErr
			}
			policyID := uuid.NewString()
			empty := json.RawMessage(`{}`)
			if _, createErr := tx.Policy.Create().Set(
				query.Policy.Id.Set(policyID), query.Policy.Name.Set("site:"+site.Id),
				query.Policy.CacheJson.Set(empty), query.Policy.CompressionJson.Set(empty),
				query.Policy.DeliveryJson.Set(empty), query.Policy.WafJson.Set(wafJSON),
			).Do(ctx); createErr != nil {
				return createErr
			}
			_, updateErr := tx.Site.Update().Where(query.Site.Id.Equals(site.Id)).Set(query.Site.PolicyId.Set(policyID)).Do(ctx)
			return updateErr
		})
		if err != nil {
			return err
		}

		response := securityPolicyResponse{WAF: input.WAF}
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
