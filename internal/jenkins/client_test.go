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
	assert.NotErrorIs(t, err, ErrAuth)
	assert.NotErrorIs(t, err, ErrNotFound)
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
