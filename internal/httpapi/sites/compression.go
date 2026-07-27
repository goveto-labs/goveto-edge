package sites

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/httpapi/types"
	compressionpolicy "goveto-edge/internal/policy"
	"goveto-edge/internal/publisher"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/query"
)

type compressionUpdateResponse struct {
	Compression  compressionpolicy.CompressionPolicy `json:"compression"`
	PublishJob   *types.PublishJob                   `json:"publish_job,omitempty"`
	PublishError string                              `json:"publish_error,omitempty"`
}

func getCompression(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		ctx := c.Request().Context()
		site, err := db.Site.FindUnique(ctx, query.Site.Id.Equals(c.Param("site_id")))
		if err != nil {
			return err
		}
		result := compressionpolicy.DefaultCompressionPolicy()
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
			if findErr != nil {
				return findErr
			}
			if len(stored.CompressionJson) > 0 && string(stored.CompressionJson) != "{}" {
				if err = json.Unmarshal(stored.CompressionJson, &result); err != nil {
					return err
				}
			}
		}
		if err = result.NormalizeAndValidate(); err != nil {
			return err
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

func updateCompression(db *client.Client, publishService *publisher.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}

		var input compressionpolicy.CompressionPolicy
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
		before := compressionpolicy.DefaultCompressionPolicy()
		if site.PolicyId != nil {
			stored, findErr := db.Policy.FindUnique(ctx, query.Policy.Id.Equals(*site.PolicyId))
			if findErr != nil {
				return findErr
			}
			if len(stored.CompressionJson) > 0 && string(stored.CompressionJson) != "{}" {
				if err = json.Unmarshal(stored.CompressionJson, &before); err != nil {
					return err
				}
			}
		}
		err = db.Tx(ctx, func(tx *client.Client) error {
			if site.PolicyId != nil {
				_, updateErr := tx.Policy.Update().
					Where(query.Policy.Id.Equals(*site.PolicyId)).
					Set(query.Policy.CompressionJson.Set(encoded)).
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
					query.Policy.CompressionJson.Set(encoded),
					query.Policy.DeliveryJson.Set(empty),
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

		response := compressionUpdateResponse{Compression: input}
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
