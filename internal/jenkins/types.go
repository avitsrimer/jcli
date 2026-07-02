package jenkins

import "errors"

// sentinel errors mapped from HTTP status codes; callers match with errors.Is and the
// CLI layer turns them into process exit codes (auth=2, not-found=3, etc.).
var (
	// ErrAuth indicates the credentials were rejected (HTTP 401).
	ErrAuth = errors.New("authentication failed")
	// ErrPermission indicates the user is authenticated but lacks access (HTTP 403).
	ErrPermission = errors.New("permission denied")
	// ErrNotFound indicates the requested job or endpoint does not exist (HTTP 404).
	ErrNotFound = errors.New("not found")
)

// Identity is the subset of /whoAmI we care about: the authenticated principal Jenkins
// reports for the current token. The token is verified at login by a successful WhoAmI call.
type Identity struct {
	Name string `json:"name"`
}

// Job is a single node in the Jenkins job tree. Folders carry nested Jobs; leaf jobs
// carry Buildable. Class is the raw Jenkins _class (e.g. WorkflowJob, Folder).
type Job struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Class     string `json:"_class"`
	Buildable bool   `json:"buildable"`
	Jobs      []Job  `json:"jobs"`
}

// Param is a normalized parameter definition lifted from a job's parameterDefinitions.
// Type is the short form (Choice/String/Boolean/...), Choices is set only for Choice
// params, and Default is the stringified default value.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Choices     []string `json:"choices,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
}

// rootResponse is the shape of GET /api/json?tree=jobs[...] including nested folders.
type rootResponse struct {
	Jobs []Job `json:"jobs"`
}

// jobDetail is the shape of GET <job>/api/json with a parameterDefinitions tree. Jenkins
// nests the definitions under one of the job's property entries.
type jobDetail struct {
	Properties []jobProperty `json:"property"`
}

// jobProperty is one entry of a job's property array; only the parametersDefinitionProperty
// entry carries ParameterDefinitions, the rest decode to an empty slice.
type jobProperty struct {
	ParameterDefinitions []parameterDefinition `json:"parameterDefinitions"`
}

// parameterDefinition is the raw Jenkins parameterDefinitions element.
type parameterDefinition struct {
	Name                  string        `json:"name"`
	Type                  string        `json:"type"`
	Description           string        `json:"description"`
	Choices               []string      `json:"choices"`
	DefaultParameterValue *defaultValue `json:"defaultParameterValue"`
}

// defaultValue wraps the defaultParameterValue object; Value is decoded as a generic so a
// Boolean default (true) or String default ("master") both survive.
type defaultValue struct {
	Value any `json:"value"`
}

// QueueItem is the subset of a Jenkins queue item jcli needs to resolve a triggered build:
// while the item is pending Executable is nil; once Jenkins starts the build, Executable
// carries the assigned build number and URL.
type QueueItem struct {
	Executable *Executable `json:"executable"`
	Cancelled  bool        `json:"cancelled"`
}

// Executable is the started build a queue item resolved to.
type Executable struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// BuildResult is the subset of a build's status jcli needs to poll to completion: Building is
// true until the run finishes, after which Result holds the terminal outcome (SUCCESS, FAILURE,
// UNSTABLE, ABORTED).
type BuildResult struct {
	Building bool   `json:"building"`
	Result   string `json:"result"`
}

// Stage is one stage of a Pipeline run as reported by the Stage View Plugin's wfapi/describe
// endpoint. Status is the stage state enum (NOT_EXECUTED, IN_PROGRESS, SUCCESS, FAILED, ABORTED,
// UNSTABLE, PAUSED) and DurationMillis is the wall-clock duration once the stage completes.
type Stage struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	DurationMillis int64  `json:"durationMillis"`
}

// stageViewResponse is the shape of GET <buildURL>/wfapi/describe: a top-level stages array in
// pipeline order. Only the fields jcli surfaces are decoded.
type stageViewResponse struct {
	Stages []Stage `json:"stages"`
}

// Build is a build's status detail read either from <buildURL>/api/json or from a job's
// lastBuild. Building is true while the run is in progress; Result holds the terminal outcome
// once finished. Timestamp is the build's start time in epoch milliseconds (used for elapsed).
type Build struct {
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Building  bool   `json:"building"`
	Result    string `json:"result"`
	Timestamp int64  `json:"timestamp"`
}

// jobLastBuild is the shape of GET <job>/api/json?tree=lastBuild[...]; LastBuild is nil for a
// job that has never run.
type jobLastBuild struct {
	LastBuild *Build `json:"lastBuild"`
}

// RunningBuild is one currently-executing build reported by /computer. Name is the executable's
// fullDisplayName, which ALREADY includes the build number (e.g. "Folder » MyJob #42") — render
// it verbatim and do not also print Number.
type RunningBuild struct {
	Name      string `json:"fullDisplayName"`
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Timestamp int64  `json:"timestamp"`
}

// computerResponse is the shape of GET /computer/api/json: one entry per node. Each node lists
// its per-stage executors and its flyweight oneOffExecutors; an idle executor has a nil
// currentExecutable.
type computerResponse struct {
	Computer []struct {
		Executors       []executor `json:"executors"`
		OneOffExecutors []executor `json:"oneOffExecutors"`
	} `json:"computer"`
}

// executor wraps the run an executor is currently carrying; CurrentExecutable is nil when idle.
type executor struct {
	CurrentExecutable *RunningBuild `json:"currentExecutable"`
}

// ConsoleChunk is one progressive slice of a build's console output from
// logText/progressiveText. Size is the next byte offset to request; More is true while the build
// is still producing output (Jenkins' X-More-Data), false once the log is complete.
type ConsoleChunk struct {
	Text string
	Size int64
	More bool
}
