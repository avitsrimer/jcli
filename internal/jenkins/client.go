// Package jenkins is a thin REST client over the Jenkins endpoints jcli needs: whoAmI for
// identity, the recursive /api/json job tree, per-job parameter definitions, and triggering
// builds via buildWithParameters. It deliberately declares no interface — the consumer
// (the cli package) owns that — and maps HTTP status codes to typed sentinel errors.
package jenkins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultTimeout bounds every request; Jenkins crawls can be slow but should never hang.
const defaultTimeout = 30 * time.Second

// maxBodySnippet caps how much of an unexpected error body we surface in wrapped errors.
const maxBodySnippet = 512

// Client talks to a single Jenkins server with one set of credentials. It is safe for
// concurrent use; the zero value is not usable — construct it with New.
type Client struct {
	baseURL  string
	username string
	token    string
	http     *http.Client
}

// New constructs a Client for baseURL authenticating as username with the API token. A nil
// httpClient falls back to a client with a sane default timeout. The trailing slash on
// baseURL, if any, is trimmed so path joins stay clean.
func New(baseURL, username, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		token:    token,
		http:     httpClient,
	}
}

// WhoAmI returns the identity Jenkins reports for the current credentials. A 401 surfaces
// as ErrAuth so callers can distinguish a bad token from other failures.
func (c *Client) WhoAmI(ctx context.Context) (Identity, error) {
	var id Identity
	if err := c.getJSON(ctx, "/whoAmI/api/json", nil, &id); err != nil {
		return Identity{}, fmt.Errorf("whoami: %w", err)
	}
	return id, nil
}

// Jobs returns the full job tree, recursively descending into folders. The tree query asks
// for nested jobs three levels deep, which covers a typical view/folder layout; deeper
// nesting simply returns empty Jobs slices.
func (c *Client) Jobs(ctx context.Context) ([]Job, error) {
	const tree = "jobs[name,url,buildable,_class," +
		"jobs[name,url,buildable,_class," +
		"jobs[name,url,buildable,_class]]]"
	var root rootResponse
	if err := c.getJSON(ctx, "/api/json", url.Values{"tree": {tree}}, &root); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return root.Jobs, nil
}

// JobParams reads the live parameter definitions for a job addressed by its Jenkins path
// (e.g. "/view/Microservices/job/Logistics" or "/job/Folder/job/Child"). It returns an empty
// slice for unparameterized jobs and ErrNotFound when the job is absent.
func (c *Client) JobParams(ctx context.Context, jobPath string) ([]Param, error) {
	const tree = "name,buildable,property[parameterDefinitions[name,type," +
		"defaultParameterValue[value],choices,description]]"
	var detail jobDetail
	path := strings.TrimRight(jobPath, "/") + "/api/json"
	if err := c.getJSON(ctx, path, url.Values{"tree": {tree}}, &detail); err != nil {
		return nil, fmt.Errorf("job params %s: %w", jobPath, err)
	}
	return normalizeParams(detail.Properties), nil
}

// Build triggers buildWithParameters for the job at jobPath with the given parameters and
// returns the queue-item location from the response Location header. Jenkins answers a
// successful trigger with 201 Created; any other status maps through the typed errors.
func (c *Client) Build(ctx context.Context, jobPath string, params map[string]string) (string, error) {
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	path := strings.TrimRight(jobPath, "/") + "/buildWithParameters"
	endpoint := c.baseURL + path
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build %s: new request: %w", jobPath, err)
	}
	req.SetBasicAuth(c.username, c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("build %s: %w", jobPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("build %s: %w", jobPath, statusError(resp))
	}
	return resp.Header.Get("Location"), nil
}

// QueueItem reads a Jenkins queue item by its absolute URL (the Location header returned by
// Build) and reports its current state. While the build is still queued Executable is nil;
// once Jenkins starts the run Executable carries the assigned build number and URL. Callers
// poll this to resolve a queue item into a concrete build.
func (c *Client) QueueItem(ctx context.Context, queueURL string) (QueueItem, error) {
	var item QueueItem
	endpoint := strings.TrimRight(queueURL, "/") + "/api/json"
	if err := c.getJSONURL(ctx, endpoint, &item); err != nil {
		return QueueItem{}, fmt.Errorf("queue item %s: %w", queueURL, err)
	}
	return item, nil
}

// BuildResult reads a build's status by its absolute URL. Building is true until the run
// finishes; once complete Result holds the terminal outcome (SUCCESS/FAILURE/UNSTABLE/ABORTED).
// Callers poll this to wait for a build to finish.
func (c *Client) BuildResult(ctx context.Context, buildURL string) (BuildResult, error) {
	var res BuildResult
	endpoint := strings.TrimRight(buildURL, "/") + "/api/json"
	if err := c.getJSONURL(ctx, endpoint, &res); err != nil {
		return BuildResult{}, fmt.Errorf("build result %s: %w", buildURL, err)
	}
	return res, nil
}

// StageView reads the Pipeline Stage View Plugin's stage breakdown for a build by its absolute
// URL, hitting <buildURL>/wfapi/describe and returning the stages in pipeline order. Jobs without
// the plugin (or freestyle jobs) answer 404, which surfaces as ErrNotFound; callers treat that as
// "no stage data" and swallow it rather than failing the wait.
func (c *Client) StageView(ctx context.Context, buildURL string) ([]Stage, error) {
	var view stageViewResponse
	endpoint := strings.TrimRight(buildURL, "/") + "/wfapi/describe"
	if err := c.getJSONURL(ctx, endpoint, &view); err != nil {
		return nil, fmt.Errorf("stage view %s: %w", buildURL, err)
	}
	return view.Stages, nil
}

// LastBuild reads a job's most recent build summary addressed by its Jenkins path. ok is false
// when the job has never built (lastBuild is null). ErrNotFound surfaces when the job is absent.
func (c *Client) LastBuild(ctx context.Context, jobPath string) (Build, bool, error) {
	const tree = "lastBuild[number,url,building,result,timestamp]"
	var detail jobLastBuild
	path := strings.TrimRight(jobPath, "/") + "/api/json"
	if err := c.getJSON(ctx, path, url.Values{"tree": {tree}}, &detail); err != nil {
		return Build{}, false, fmt.Errorf("last build %s: %w", jobPath, err)
	}
	if detail.LastBuild == nil {
		return Build{}, false, nil
	}
	return *detail.LastBuild, true, nil
}

// BuildStatus reads a single build's status by its absolute URL, returning its number, building
// flag, terminal result, and start timestamp. ErrNotFound surfaces when the build is absent.
func (c *Client) BuildStatus(ctx context.Context, buildURL string) (Build, error) {
	const tree = "number,url,building,result,timestamp"
	var b Build
	endpoint := strings.TrimRight(buildURL, "/") + "/api/json?" + url.Values{"tree": {tree}}.Encode()
	if err := c.getJSONURL(ctx, endpoint, &b); err != nil {
		return Build{}, fmt.Errorf("build status %s: %w", buildURL, err)
	}
	return b, nil
}

// RunningBuilds lists every currently-executing build across all nodes via /computer/api/json,
// reading both the per-stage executors and the flyweight oneOffExecutors. Idle executors (nil
// currentExecutable) are skipped and entries are deduped by build URL, preferring the
// oneOffExecutors record whose number/timestamp/fullDisplayName are reliable for pipeline runs.
func (c *Client) RunningBuilds(ctx context.Context) ([]RunningBuild, error) {
	const tree = "computer[executors[currentExecutable[number,url,fullDisplayName,timestamp]]," +
		"oneOffExecutors[currentExecutable[number,url,fullDisplayName,timestamp]]]"
	var resp computerResponse
	if err := c.getJSON(ctx, "/computer/api/json", url.Values{"tree": {tree}}, &resp); err != nil {
		return nil, fmt.Errorf("running builds: %w", err)
	}

	seen := map[string]int{}
	var out []RunningBuild
	// oneOffExecutors first so a flyweight record wins the dedupe over a node-executor placeholder.
	add := func(execs []executor) {
		for _, e := range execs {
			rb := e.CurrentExecutable
			if rb == nil || rb.URL == "" {
				continue
			}
			if _, dup := seen[rb.URL]; dup {
				continue
			}
			seen[rb.URL] = len(out)
			out = append(out, *rb)
		}
	}
	for _, node := range resp.Computer {
		add(node.OneOffExecutors)
	}
	for _, node := range resp.Computer {
		add(node.Executors)
	}
	return out, nil
}

// getJSON performs an authenticated GET against a baseURL-relative path, maps status codes to
// typed errors, and decodes a 200 body into out. query may be nil.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return c.getJSONURL(ctx, endpoint, out)
}

// getJSONURL performs an authenticated GET against an absolute endpoint URL, maps status codes
// to typed errors, and decodes a 200 body into out. Used for queue/build URLs which Jenkins
// hands back as absolute URLs rather than baseURL-relative paths.
func (c *Client) getJSONURL(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.SetBasicAuth(c.username, c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// statusError maps an HTTP status to a sentinel where one applies, else a wrapped error with
// a bounded body snippet for diagnostics. It consumes a bounded amount of the body.
func statusError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden:
		return ErrPermission
	case http.StatusNotFound:
		return ErrNotFound
	}
	snippet := readSnippet(resp.Body)
	if snippet == "" {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, snippet)
}

// readSnippet reads up to maxBodySnippet bytes and trims whitespace for error messages.
func readSnippet(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, maxBodySnippet))
	return strings.TrimSpace(string(data))
}

// normalizeParams flattens the parameterDefinitions found in any job property into the
// public Param shape, shortening the type and stringifying the default value.
func normalizeParams(props []jobProperty) []Param {
	var out []Param
	for _, p := range props {
		for _, def := range p.ParameterDefinitions {
			param := Param{
				Name:        def.Name,
				Type:        strings.TrimSuffix(def.Type, "ParameterDefinition"),
				Choices:     def.Choices,
				Description: def.Description,
			}
			if def.DefaultParameterValue != nil {
				param.Default = stringifyValue(def.DefaultParameterValue.Value)
			}
			out = append(out, param)
		}
	}
	return out
}

// stringifyValue renders a JSON default value (string, bool, or number) as a string so it
// can be sent back verbatim as a build parameter.
func stringifyValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}
