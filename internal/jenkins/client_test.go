package jenkins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stageViewBody mirrors a wfapi/describe response with stages in pipeline order and mixed statuses.
const stageViewBody = `{
  "_links": {"self": {"href": "/job/Pipeline/7/wfapi/describe"}},
  "name": "#7",
  "status": "IN_PROGRESS",
  "stages": [
    {"name": "Checkout", "status": "SUCCESS", "durationMillis": 1200},
    {"name": "Build", "status": "SUCCESS", "durationMillis": 45000},
    {"name": "Deploy", "status": "IN_PROGRESS", "durationMillis": 0}
  ]
}`

// whoAmIBody mirrors the real /whoAmI/api/json shape captured in research.
const whoAmIBody = `{
  "_class": "hudson.security.WhoAmI",
  "name": "ci-user",
  "authenticated": true,
  "authorities": ["authenticated", "developers"]
}`

// jobTreeBody mirrors a recursive /api/json?tree=jobs[...] response with a folder/view that
// nests buildable jobs, exercising the recursive Jobs decode.
const jobTreeBody = `{
  "_class": "hudson.model.Hudson",
  "jobs": [
    {
      "_class": "com.cloudbees.hudson.plugins.folder.Folder",
      "name": "Microservices",
      "url": "https://jenkins/job/Microservices/",
      "jobs": [
        {
          "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob",
          "name": "Logistics",
          "url": "https://jenkins/job/Microservices/job/Logistics/",
          "color": "blue",
          "buildable": true
        }
      ]
    },
    {
      "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob",
      "name": "UAT1-App-Deployment-pipeline",
      "url": "https://jenkins/job/UAT1-App-Deployment-pipeline/",
      "color": "blue",
      "buildable": true
    }
  ]
}`

// jobParamsBody mirrors a job detail with Choice, String, and Boolean parameter defs.
const jobParamsBody = `{
  "_class": "org.jenkinsci.plugins.workflow.job.WorkflowJob",
  "name": "Logistics",
  "buildable": true,
  "property": [
    {"_class": "com.example.SomeOtherProperty"},
    {
      "_class": "hudson.model.ParametersDefinitionProperty",
      "parameterDefinitions": [
        {
          "_class": "hudson.model.ChoiceParameterDefinition",
          "name": "service",
          "type": "ChoiceParameterDefinition",
          "choices": ["supplier_stock", "shipment_status"],
          "defaultParameterValue": {"value": "supplier_stock"}
        },
        {
          "_class": "hudson.model.StringParameterDefinition",
          "name": "branch",
          "type": "StringParameterDefinition",
          "description": "git branch",
          "defaultParameterValue": {"value": "master"}
        },
        {
          "_class": "hudson.model.BooleanParameterDefinition",
          "name": "public_api",
          "type": "BooleanParameterDefinition",
          "defaultParameterValue": {"value": true}
        }
      ]
    }
  ]
}`

func TestClient_WhoAmI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/whoAmI/api/json", r.URL.Path)
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "alice", user)
		assert.Equal(t, "tok", pass)
		_, _ = w.Write([]byte(whoAmIBody))
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	id, err := c.WhoAmI(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ci-user", id.Name)
}

func TestClient_Jobs_Recursive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/json", r.URL.Path)
		assert.Contains(t, r.URL.Query().Get("tree"), "jobs[name,url,buildable,_class")
		_, _ = w.Write([]byte(jobTreeBody))
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	jobs, err := c.Jobs(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 2)

	folder := jobs[0]
	assert.Equal(t, "Microservices", folder.Name)
	assert.Contains(t, folder.Class, "Folder")
	require.Len(t, folder.Jobs, 1)
	assert.Equal(t, "Logistics", folder.Jobs[0].Name)
	assert.True(t, folder.Jobs[0].Buildable)

	assert.Equal(t, "UAT1-App-Deployment-pipeline", jobs[1].Name)
	assert.True(t, jobs[1].Buildable)
}

func TestClient_JobParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/view/Microservices/job/Logistics/api/json", r.URL.Path)
		_, _ = w.Write([]byte(jobParamsBody))
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	params, err := c.JobParams(context.Background(), "/view/Microservices/job/Logistics")
	require.NoError(t, err)
	require.Len(t, params, 3)

	assert.Equal(t, "service", params[0].Name)
	assert.Equal(t, "Choice", params[0].Type)
	assert.Equal(t, []string{"supplier_stock", "shipment_status"}, params[0].Choices)
	assert.Equal(t, "supplier_stock", params[0].Default)

	assert.Equal(t, "branch", params[1].Name)
	assert.Equal(t, "String", params[1].Type)
	assert.Equal(t, "master", params[1].Default)
	assert.Equal(t, "git branch", params[1].Description)

	assert.Equal(t, "public_api", params[2].Name)
	assert.Equal(t, "Boolean", params[2].Type)
	assert.Equal(t, "true", params[2].Default)
}

func TestClient_JobParams_Unparameterized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"NoParams","buildable":true,"property":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	params, err := c.JobParams(context.Background(), "/job/NoParams")
	require.NoError(t, err)
	assert.Empty(t, params)
}

func TestClient_Build_Success(t *testing.T) {
	const location = "https://jenkins/queue/item/42/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/view/Microservices/job/Logistics/buildWithParameters", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "supplier_stock", q.Get("service"))
		assert.Equal(t, "uat1", q.Get("stage"))
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	loc, err := c.Build(context.Background(), "/view/Microservices/job/Logistics",
		map[string]string{"service": "supplier_stock", "stage": "uat1"})
	require.NoError(t, err)
	assert.Equal(t, location, loc)
}

func TestClient_QueueItem(t *testing.T) {
	t.Run("pending item has no executable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/queue/item/42/api/json", r.URL.Path)
			_, _ = w.Write([]byte(`{"_class":"hudson.model.Queue$WaitingItem","why":"In the quiet period","executable":null}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		item, err := c.QueueItem(context.Background(), srv.URL+"/queue/item/42/")
		require.NoError(t, err)
		assert.Nil(t, item.Executable)
		assert.False(t, item.Cancelled)
	})

	t.Run("started item carries the build number", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"_class":"hudson.model.Queue$LeftItem",` +
				`"executable":{"number":7,"url":"https://jenkins/job/Logistics/7/"}}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		item, err := c.QueueItem(context.Background(), srv.URL+"/queue/item/42/")
		require.NoError(t, err)
		require.NotNil(t, item.Executable)
		assert.Equal(t, 7, item.Executable.Number)
		assert.Equal(t, "https://jenkins/job/Logistics/7/", item.Executable.URL)
	})
}

func TestClient_BuildResult(t *testing.T) {
	t.Run("building run has no result yet", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Logistics/7/api/json", r.URL.Path)
			_, _ = w.Write([]byte(`{"number":7,"building":true,"result":null}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		res, err := c.BuildResult(context.Background(), srv.URL+"/job/Logistics/7/")
		require.NoError(t, err)
		assert.True(t, res.Building)
		assert.Empty(t, res.Result)
	})

	t.Run("finished run reports terminal result", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"number":7,"building":false,"result":"SUCCESS"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		res, err := c.BuildResult(context.Background(), srv.URL+"/job/Logistics/7/")
		require.NoError(t, err)
		assert.False(t, res.Building)
		assert.Equal(t, "SUCCESS", res.Result)
	})
}

func TestClient_StageView(t *testing.T) {
	t.Run("decodes stages in pipeline order with mixed statuses", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Pipeline/7/wfapi/describe", r.URL.Path)
			_, _ = w.Write([]byte(stageViewBody))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		stages, err := c.StageView(context.Background(), srv.URL+"/job/Pipeline/7/")
		require.NoError(t, err)
		require.Len(t, stages, 3)

		assert.Equal(t, "Checkout", stages[0].Name)
		assert.Equal(t, "SUCCESS", stages[0].Status)
		assert.Equal(t, int64(1200), stages[0].DurationMillis)

		assert.Equal(t, "Build", stages[1].Name)
		assert.Equal(t, int64(45000), stages[1].DurationMillis)

		assert.Equal(t, "Deploy", stages[2].Name)
		assert.Equal(t, "IN_PROGRESS", stages[2].Status)
	})

	t.Run("404 surfaces as ErrNotFound for callers to swallow", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.StageView(context.Background(), srv.URL+"/job/Freestyle/3/")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("malformed body is a decode error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"stages": [ not json`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.StageView(context.Background(), srv.URL+"/job/Pipeline/7/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode response")
	})
}

func TestClient_LastBuild(t *testing.T) {
	t.Run("running job returns its latest build", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Logistics/api/json", r.URL.Path)
			assert.Contains(t, r.URL.Query().Get("tree"), "lastBuild[number,url,building,result,timestamp]")
			_, _ = w.Write([]byte(`{"lastBuild":{"number":42,"url":"https://jenkins/job/Logistics/42/",` +
				`"building":true,"result":null,"timestamp":1700000000000}}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		b, ok, err := c.LastBuild(context.Background(), "/job/Logistics")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, 42, b.Number)
		assert.True(t, b.Building)
		assert.Equal(t, "https://jenkins/job/Logistics/42/", b.URL)
		assert.Equal(t, int64(1700000000000), b.Timestamp)
	})

	t.Run("never-built job reports ok=false", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"lastBuild":null}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, ok, err := c.LastBuild(context.Background(), "/job/Fresh")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("absent job surfaces ErrNotFound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, _, err := c.LastBuild(context.Background(), "/job/Nope")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

func TestClient_BuildStatus(t *testing.T) {
	t.Run("finished build reports result and timestamp", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Logistics/42/api/json", r.URL.Path)
			assert.Contains(t, r.URL.Query().Get("tree"), "number,url,building,result,timestamp")
			_, _ = w.Write([]byte(`{"number":42,"building":false,"result":"SUCCESS","timestamp":1700000000000}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		b, err := c.BuildStatus(context.Background(), srv.URL+"/job/Logistics/42/")
		require.NoError(t, err)
		assert.Equal(t, 42, b.Number)
		assert.False(t, b.Building)
		assert.Equal(t, "SUCCESS", b.Result)
		assert.Equal(t, int64(1700000000000), b.Timestamp)
	})

	t.Run("absent build surfaces ErrNotFound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.BuildStatus(context.Background(), srv.URL+"/job/Logistics/999/")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// buildParamsBody mirrors GET <buildURL>/api/json?tree=actions[parameters[name,value]]: a
// ParametersAction (a String and a Boolean value) interleaved with empty action objects.
const buildParamsBody = `{
  "actions": [
    {},
    {"_class": "hudson.model.CauseAction"},
    {
      "_class": "hudson.model.ParametersAction",
      "parameters": [
        {"_class": "hudson.model.StringParameterValue", "name": "raven_branch", "value": "master"},
        {"_class": "hudson.model.BooleanParameterValue", "name": "run_migrations", "value": true}
      ]
    },
    {}
  ]
}`

func TestClient_BuildParams(t *testing.T) {
	t.Run("flattens parameters in order with stringified values", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Raven/1828/api/json", r.URL.Path)
			assert.Equal(t, "actions[parameters[name,value]]", r.URL.Query().Get("tree"))
			_, _ = w.Write([]byte(buildParamsBody))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		params, err := c.BuildParams(context.Background(), srv.URL+"/job/Raven/1828/")
		require.NoError(t, err)
		require.Len(t, params, 2)

		assert.Equal(t, "raven_branch", params[0].Name)
		assert.Equal(t, "master", params[0].Value)

		assert.Equal(t, "run_migrations", params[1].Name)
		assert.Equal(t, "true", params[1].Value)
	})

	t.Run("build with no parameters yields empty slice", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"actions":[{},{"_class":"hudson.model.CauseAction"}]}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		params, err := c.BuildParams(context.Background(), srv.URL+"/job/Free/3/")
		require.NoError(t, err)
		assert.Empty(t, params)
	})

	t.Run("duplicate parameter across actions is deduped, last value wins in first position", func(t *testing.T) {
		// two actions each carry a "raven_branch" parameter (a rebuild/plugin quirk); the human slice
		// must list it once at its first position with the last-seen value, matching the --json map.
		const body = `{
		  "actions": [
		    {"_class": "hudson.model.ParametersAction",
		     "parameters": [
		       {"name": "raven_branch", "value": "master"},
		       {"name": "where_to_deploy", "value": "uat-2"}
		     ]},
		    {"_class": "hudson.model.ParametersAction",
		     "parameters": [
		       {"name": "raven_branch", "value": "hotfix"}
		     ]}
		  ]
		}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		params, err := c.BuildParams(context.Background(), srv.URL+"/job/Raven/1/")
		require.NoError(t, err)
		require.Len(t, params, 2)
		assert.Equal(t, "raven_branch", params[0].Name)
		assert.Equal(t, "hotfix", params[0].Value)
		assert.Equal(t, "where_to_deploy", params[1].Name)
		assert.Equal(t, "uat-2", params[1].Value)
	})

	t.Run("absent build surfaces ErrNotFound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.BuildParams(context.Background(), srv.URL+"/job/Raven/999/")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("malformed body is a decode error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"actions": [ not json`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.BuildParams(context.Background(), srv.URL+"/job/Raven/1/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode response")
	})
}

func TestClient_RunningBuilds(t *testing.T) {
	t.Run("collects executors, skips idle, dedupes preferring oneOff", func(t *testing.T) {
		// a pipeline run appears both as a node-executor placeholder (no metadata) and as a
		// flyweight oneOffExecutor (full metadata) at the same URL; a freestyle run appears once
		// in a node executor; one executor is idle.
		const body = `{"computer":[
		  {
		    "executors":[
		      {"currentExecutable":{"number":0,"url":"https://jenkins/job/Pipe/7/","fullDisplayName":"","timestamp":0}},
		      {"currentExecutable":{"number":3,"url":"https://jenkins/job/Free/3/","fullDisplayName":"Free #3","timestamp":1700000003000}},
		      {"currentExecutable":null}
		    ],
		    "oneOffExecutors":[
		      {"currentExecutable":{"number":7,"url":"https://jenkins/job/Pipe/7/","fullDisplayName":"Pipe #7","timestamp":1700000007000}}
		    ]
		  }
		]}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/computer/api/json", r.URL.Path)
			assert.Contains(t, r.URL.Query().Get("tree"), "oneOffExecutors[currentExecutable")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		builds, err := c.RunningBuilds(context.Background())
		require.NoError(t, err)
		require.Len(t, builds, 2)

		byURL := map[string]RunningBuild{}
		for _, b := range builds {
			byURL[b.URL] = b
		}
		// the oneOff record won the dedupe for the pipeline URL.
		pipe := byURL["https://jenkins/job/Pipe/7/"]
		assert.Equal(t, 7, pipe.Number)
		assert.Equal(t, "Pipe #7", pipe.Name)
		assert.Equal(t, int64(1700000007000), pipe.Timestamp)

		free := byURL["https://jenkins/job/Free/3/"]
		assert.Equal(t, "Free #3", free.Name)
	})

	t.Run("no executors running yields empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"computer":[{"executors":[{"currentExecutable":null}],"oneOffExecutors":[]}]}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		builds, err := c.RunningBuilds(context.Background())
		require.NoError(t, err)
		assert.Empty(t, builds)
	})
}

func TestClient_ConsoleText(t *testing.T) {
	t.Run("returns the full console body without a json accept header", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Logistics/42/consoleText", r.URL.Path)
			assert.NotEqual(t, "application/json", r.Header.Get("Accept"))
			_, _ = w.Write([]byte("Started\nRunning tests\nFinished: SUCCESS\n"))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		text, err := c.ConsoleText(context.Background(), srv.URL+"/job/Logistics/42/")
		require.NoError(t, err)
		assert.Contains(t, text, "Finished: SUCCESS")
	})

	t.Run("absent build surfaces ErrNotFound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.ConsoleText(context.Background(), srv.URL+"/job/Logistics/999/")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("401 surfaces ErrAuth", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		_, err := c.ConsoleText(context.Background(), srv.URL+"/job/Logistics/1/")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrAuth)
	})
}

func TestClient_ConsoleProgressive(t *testing.T) {
	t.Run("parses text-size and more-data headers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/job/Logistics/42/logText/progressiveText", r.URL.Path)
			assert.Equal(t, "10", r.URL.Query().Get("start"))
			w.Header().Set("X-Text-Size", "42")
			w.Header().Set("X-More-Data", "true")
			_, _ = w.Write([]byte("more output"))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		chunk, err := c.ConsoleProgressive(context.Background(), srv.URL+"/job/Logistics/42/", 10)
		require.NoError(t, err)
		assert.Equal(t, "more output", chunk.Text)
		assert.Equal(t, int64(42), chunk.Size)
		assert.True(t, chunk.More)
	})

	t.Run("no more-data header means finished", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Text-Size", "100")
			_, _ = w.Write([]byte("tail"))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		chunk, err := c.ConsoleProgressive(context.Background(), srv.URL+"/job/Logistics/42/", 96)
		require.NoError(t, err)
		assert.False(t, chunk.More)
		assert.Equal(t, int64(100), chunk.Size)
	})

	t.Run("size header absent falls back to start plus body length", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("12345"))
		}))
		defer srv.Close()

		c := New(srv.URL, "alice", "tok", srv.Client())
		chunk, err := c.ConsoleProgressive(context.Background(), srv.URL+"/job/Logistics/42/", 7)
		require.NoError(t, err)
		assert.Equal(t, int64(12), chunk.Size, "start(7) + len(\"12345\")")
	})
}

func TestClient_StatusMapping(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrAuth},
		{"forbidden", http.StatusForbidden, ErrPermission},
		{"notfound", http.StatusNotFound, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			c := New(srv.URL, "alice", "tok", srv.Client())
			_, err := c.WhoAmI(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestClient_ServerError_BodySnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom: internal failure"))
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	_, err := c.Jobs(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAuth)
	require.NotErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "boom: internal failure")
}

func TestClient_Build_ErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, "alice", "tok", srv.Client())
	_, err := c.Build(context.Background(), "/job/Nope", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestClient_TrimsTrailingSlash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/whoAmI/api/json", r.URL.Path)
		_, _ = w.Write([]byte(whoAmIBody))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "alice", "tok", srv.Client())
	_, err := c.WhoAmI(context.Background())
	require.NoError(t, err)
}
