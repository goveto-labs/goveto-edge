package purge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/audit"
	"goveto-edge/internal/auth"
	"goveto-edge/internal/clusteraccess"
	"goveto-edge/internal/httpapi/types"
	"goveto-edge/internal/jobqueue"
	"goveto-edge/internal/purge"
	"goveto-edge/internal/rbac"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func Register(e *echo.Echo, db *client.Client, service *purge.Service) {
	read := clusteraccess.RequirePermission(db, rbac.PermissionClusterRead)
	operate := clusteraccess.RequirePermission(db, rbac.PermissionCacheOperate)
	e.POST("/api/v1/clusters/:cluster_id/sites/:site_id/purge", enqueue(db, service), auth.RequireAuth, operate)
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/purge", list(db), auth.RequireAuth, read)
	e.GET("/api/v1/clusters/:cluster_id/sites/:site_id/purge/:job_id", get(db), auth.RequireAuth, read)
	e.POST("/api/v1/clusters/:cluster_id/sites/:site_id/prewarm", prewarm(db), auth.RequireAuth, operate)
}

type request struct {
	Type  model.PurgeType `json:"type"`
	Value *string         `json:"value"`
}

type prewarmRequest struct {
	URLs []string `json:"urls"`
}

type prewarmResult struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

func prewarm(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}
		var input prewarmRequest
		if err = c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if len(input.URLs) == 0 || len(input.URLs) > 20 {
			return echo.NewHTTPError(http.StatusBadRequest, "provide between 1 and 20 URLs")
		}

		domains, err := db.SiteDomain.Query().
			Where(query.SiteDomain.SiteId.Equals(site.Id)).
			Do(c.Request().Context())
		if err != nil {
			return err
		}
		allowedHosts := make(map[string]struct{}, len(domains))
		for index := range domains {
			allowedHosts[strings.ToLower(domains[index].Hostname)] = struct{}{}
		}

		normalized := make([]string, len(input.URLs))
		for index, rawURL := range input.URLs {
			parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid prewarm URL %q", rawURL))
			}
			if _, allowed := allowedHosts[strings.ToLower(parsed.Hostname())]; !allowed {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("URL host %q is not a site domain", parsed.Hostname()))
			}
			parsed.Fragment = ""
			normalized[index] = parsed.String()
		}

		client := newPrewarmClient(allowedHosts)
		defer client.CloseIdleConnections()
		ctx := c.Request().Context()
		results := make([]prewarmResult, len(normalized))
		semaphore := make(chan struct{}, 4)
		var wait sync.WaitGroup
		for index, target := range normalized {
			wait.Add(1)
			go func() {
				defer wait.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				results[index] = fetchPrewarm(ctx, client, target)
			}()
		}
		wait.Wait()
		audit.SetChange(c, input, results)
		return types.JSON(c, http.StatusOK, results)
	}
}

func newPrewarmClient(allowedHosts map[string]struct{}) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, address := range addresses {
				if !publicIP(address) {
					return nil, fmt.Errorf("prewarm target resolved to a non-public address")
				}
			}
			if len(addresses) == 0 {
				return nil, fmt.Errorf("prewarm target did not resolve")
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
		},
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if _, allowed := allowedHosts[strings.ToLower(req.URL.Hostname())]; !allowed {
				return fmt.Errorf("redirect target is not a site domain")
			}
			return nil
		},
	}
}

func publicIP(address net.IP) bool {
	return !address.IsLoopback() && !address.IsPrivate() && !address.IsUnspecified() &&
		!address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsMulticast()
}

func fetchPrewarm(ctx context.Context, client *http.Client, target string) prewarmResult {
	result := prewarmResult{URL: target}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	request.Header.Set("User-Agent", "Goveto-Prewarm/1.0")
	response, err := client.Do(request)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer response.Body.Close()
	_, copyErr := io.Copy(io.Discard, response.Body)
	result.StatusCode = response.StatusCode
	result.Success = response.StatusCode >= 200 && response.StatusCode < 400 && copyErr == nil
	if copyErr != nil {
		result.Error = copyErr.Error()
	}
	return result
}

// @summary Enqueue purge
// @description Enqueue a cache purge job for the site.
// @Tags purge
func enqueue(db *client.Client, service *purge.Service) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}

		var input request
		if err = c.Bind(&input); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		if input.Value != nil {
			value := strings.TrimSpace(*input.Value)
			input.Value = &value
		}

		job, err := service.EnqueueIdempotent(c.Request().Context(), site.Id, input.Type, input.Value, strings.TrimSpace(c.Request().Header.Get("Idempotency-Key")))
		if err != nil {
			switch {
			case errors.Is(err, purge.ErrInvalidRequest), errors.Is(err, jobqueue.ErrInvalidIdempotencyKey):
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			case errors.Is(err, jobqueue.ErrIdempotencyConflict):
				return echo.NewHTTPError(http.StatusConflict, err.Error())
			default:
				return err
			}
		}
		response := types.NewPurgeJob(job)
		audit.SetChange(c, input, response)
		return types.JSON(c, http.StatusAccepted, response)
	}
}

// @summary List purge jobs
// @description List recent cache purge jobs for a site.
// @Tags purge
func list(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}

		jobs, err := db.PurgeJob.Query().
			Where(query.PurgeJob.SiteId.Equals(site.Id)).
			OrderBy(query.PurgeJob.CreatedAt.Desc()).
			Take(50).
			Do(c.Request().Context())
		if err != nil {
			return err
		}

		result := make([]types.PurgeJob, 0, len(jobs))
		for i := range jobs {
			result = append(result, types.NewPurgeJob(&jobs[i]))
		}
		return types.JSON(c, http.StatusOK, result)
	}
}

// @summary Get purge job
// @description Get a single cache purge job by id.
// @Tags purge
func get(db *client.Client) echo.HandlerFunc {
	return func(c *echo.Context) error {
		site, err := siteInCluster(c, db)
		if err != nil {
			return err
		}

		job, err := db.PurgeJob.FindUnique(c.Request().Context(), query.PurgeJob.Id.Equals(c.Param("job_id")))
		if err != nil || job.SiteId != site.Id {
			return echo.NewHTTPError(http.StatusNotFound, "purge job not found")
		}
		return types.JSON(c, http.StatusOK, types.NewPurgeJob(job))
	}
}
func siteInCluster(c *echo.Context, db *client.Client) (*model.Site, error) {
	site, err := db.Site.FindUnique(c.Request().Context(), query.Site.Id.Equals(c.Param("site_id")))
	if err != nil || site.ClusterId != c.Param("cluster_id") {
		return nil, echo.NewHTTPError(http.StatusNotFound, "site not found")
	}
	return site, nil
}
