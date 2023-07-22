package venom

import (
	"context"
	"fmt"
	"github.com/ovh/cds/sdk/interpolate"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v2"
)

func TestProcessVariableAssignments(t *testing.T) {
	InitTestLogger(t)
	assign := AssignStep{}
	assign.Assignments = make(map[string]Assignment)
	assign.Assignments["assignVar"] = Assignment{
		From: "here.some.value",
	}
	assign.Assignments["assignVarWithRegex"] = Assignment{
		From:  "here.some.value",
		Regex: `this is (?s:(.*))`,
	}

	b, _ := yaml.Marshal(assign)
	t.Log("\n" + string(b))

	tcVars := H{"here.some.value": "this is the \nvalue"}

	result, is, err := processVariableAssignments(context.TODO(), "", &tcVars, b)
	assert.True(t, is)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	t.Log(result)
	assert.Equal(t, "map[assignVar:this is the \nvalue assignVarWithRegex:the \nvalue]", fmt.Sprint(result))

	var wrongStepIn TestStep
	b = []byte(`type: exec
script: echo 'foo'
`)
	assert.NoError(t, yaml.Unmarshal(b, &wrongStepIn))
	result, is, err = processVariableAssignments(context.TODO(), "", &tcVars, b)
	assert.False(t, is)
	assert.NoError(t, err)
	assert.Nil(t, result)
	assert.Empty(t, result)
}

func TestInterpolation(t *testing.T) {
	vars := map[string]string{}
	vars["input.access_token"] = "1234"
	vars["service.url"] = "example.com"
	vars["input.retry"] = "0"
	vars["input.timeout"] = "30"

	payload := `
{
    "assertions": [
        "result.statuscode MustEqual 200"
    ],
    "delay": "{{.input.delay}}",
    "headers": {
        "Authorization": "Bearer {{.input.access_token}}",
        "Content-Type": "application/json;charset=UTF-8"
    },
    "info": [
        "GET USERS TRACING : {{.result.headers.interactionId}}"
    ],
    "method": "GET",
    "retry": "{{.input.retry}}",
    "timeout": "{{.input.timeout}}",
    "tls_client_cert": '{{.certs.client | default ""}}',
    "tls_client_key": '{{.certs.key | default ""}}',
    "tls_root_ca": '{{.certs.rootCa | default ""}}',
    "type": "http",
    "url": "{{.service.url}}/users",
    "vars": {
        "first": {
            "from": "result.bodyjson.bodyjson0.uuid"
        },
        "length": {
            "from": "result.bodyjson.__Len__"
        }
    }
}
`
	content, error := interpolate.Do(payload, vars)
	assert.Nil(t, error)
	assert.NotEmpty(t, content)
}
