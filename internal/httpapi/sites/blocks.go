package sites

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/redis/go-redis/v9"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/securitystate"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
)

type blockMutationRequest struct {
	Scope           string `json:"scope"`
	Address         string `json:"address"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type temporaryBlock = securitystate.TemporaryBlock

func createTemporaryBlock(db *client.Client, redisClient *redis.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if redisClient == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
		}
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		var input blockMutationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		scope, address, err := normalizeTemporaryBlock(input.Scope, input.Address)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err = requireGlobalBlockAdmin(c, scope); err != nil {
			return err
		}
		if input.DurationSeconds < 1 || input.DurationSeconds > 86400 {
			return echo.NewHTTPError(http.StatusBadRequest, "duration_seconds must be between 1 and 86400")
		}
		input.Reason = strings.TrimSpace(input.Reason)
		if len(input.Reason) > 512 {
			return echo.NewHTTPError(http.StatusBadRequest, "reason cannot exceed 512 bytes")
		}
		now := time.Now().UTC()
		record := temporaryBlock{
			Scope: scope, Address: address.String(), Reason: input.Reason,
			CreatedBy: auth.CurrentUID(c), CreatedAt: now,
			ExpiresAt: now.Add(time.Duration(input.DurationSeconds) * time.Second),
		}
		key := securitystate.GlobalBlockKey(address)
		if scope == "SITE" {
			record.SiteID = c.Param("site_id")
			key = securitystate.SiteBlockKey(record.SiteID, address)
		}
		encoded, _ := json.Marshal(record)
		if err = redisClient.Set(c.Request().Context(), key, encoded, time.Duration(input.DurationSeconds)*time.Second).Err(); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
		}
		audit.SetResourceID(c, scope+":"+record.Address)
		audit.SetChange(c, nil, record)
		return types.JSON(c, http.StatusCreated, record)
	}
}

func deleteTemporaryBlock(db *client.Client, redisClient *redis.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if redisClient == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
		}
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		var input blockMutationRequest
		if err := c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		scope, address, err := normalizeTemporaryBlock(input.Scope, input.Address)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err = requireGlobalBlockAdmin(c, scope); err != nil {
			return err
		}
		key := securitystate.GlobalBlockKey(address)
		if scope == "SITE" {
			key = securitystate.SiteBlockKey(c.Param("site_id"), address)
		}
		removed, err := redisClient.Del(c.Request().Context(), key).Result()
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
		}
		if removed == 0 {
			return echo.NewHTTPError(http.StatusNotFound, "temporary block not found")
		}
		audit.SetResourceID(c, scope+":"+address.String())
		return c.NoContent(http.StatusNoContent)
	}
}

func listTemporaryBlocks(db *client.Client, redisClient *redis.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if redisClient == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
		}
		if err := ensureSiteInCluster(c, db); err != nil {
			return err
		}
		patterns := []string{"block:site:" + c.Param("site_id") + ":*"}
		if user, ok := auth.CurrentUser(c.Request().Context(), auth.CurrentUID(c)); ok && user.Role == model.UserRoleADMIN {
			patterns = append(patterns, "block:global:*")
		}
		items := make([]temporaryBlock, 0)
		for _, pattern := range patterns {
			var cursor uint64
			for {
				keys, next, err := redisClient.Scan(c.Request().Context(), cursor, pattern, 100).Result()
				if err != nil {
					return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
				}
				for _, key := range keys {
					data, getErr := redisClient.Get(c.Request().Context(), key).Bytes()
					if errors.Is(getErr, redis.Nil) {
						continue
					}
					if getErr != nil {
						return echo.NewHTTPError(http.StatusServiceUnavailable, "temporary block storage unavailable")
					}
					var item temporaryBlock
					if json.Unmarshal(data, &item) == nil {
						items = append(items, item)
					}
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ExpiresAt.Before(items[j].ExpiresAt) })
		return types.JSON(c, http.StatusOK, items)
	}
}

func normalizeTemporaryBlock(scope, rawAddress string) (string, netip.Addr, error) {
	scope = strings.ToUpper(strings.TrimSpace(scope))
	if scope == "" {
		scope = "SITE"
	}
	if scope != "SITE" && scope != "GLOBAL" {
		return "", netip.Addr{}, errors.New("scope must be SITE or GLOBAL")
	}
	address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(rawAddress), "[]"))
	if err != nil {
		return "", netip.Addr{}, errors.New("address must be an IPv4 or IPv6 address")
	}
	return scope, address.Unmap(), nil
}

func requireGlobalBlockAdmin(c *echo.Context, scope string) error {
	if scope != "GLOBAL" {
		return nil
	}
	user, ok := auth.CurrentUser(c.Request().Context(), auth.CurrentUID(c))
	if !ok || user.Role != model.UserRoleADMIN {
		return echo.NewHTTPError(http.StatusForbidden, "administrator role required for global blocks")
	}
	return nil
}
