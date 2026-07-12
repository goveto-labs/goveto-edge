package sites

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	cachepolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

// @summary Get site cache policy
// @description Get the site cache policy (defaults when none stored).
// @Tags sites
func getCache(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}

		site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}

		result := cachepolicy.DefaultCachePolicy()
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(
				c.Request().Context(),
				query.Policy.Id.Equals(*site.PolicyId),
			)
			if findErr != nil {
				return findErr
			}
			if err = json.Unmarshal(stored.CacheJson, &result); err != nil {
				return err
			}
		}
		return c.JSON(http.StatusOK, result)
	}
}

// @summary Update site cache policy
// @description Create or update the site cache policy and enqueue publish.
// @Tags sites
func updateCache(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}

		var input cachepolicy.CachePolicy
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if err := input.NormalizeAndValidate(); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		encoded, err := json.Marshal(input)
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
					Set(query.Policy.CacheJson.Set(encoded)).
					Do(ctx)
				return updateErr
			}

			policyID := uuid.NewString()
			empty := json.RawMessage(`{}`)
			if _, createErr := tx.Policy.Create().
				Set(
					query.Policy.Id.Set(policyID),
					query.Policy.Name.Set("site:"+site.Id),
					query.Policy.CacheJson.Set(encoded),
					query.Policy.WafJson.Set(empty),
					query.Policy.CcJson.Set(empty),
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

		response := map[string]any{"cache": input}
		if job, publishErr := publishService.Enqueue(ctx, site.Id); publishErr == nil {
			response["publish_job"] = job
		} else {
			response["publish_error"] = publishErr.Error()
		}
		return c.JSON(http.StatusOK, response)
	}
}
