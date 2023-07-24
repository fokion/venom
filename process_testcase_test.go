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
        "result.statuscode MustEqual {{.input.expected_status_code}}"
    ],
    "delay": "{{.input.delay}}",
    "headers": {
        "Authorization": "Bearer {{.input.access_token}}",
        "Consent": "{{.input.consent_token}}",
        "Content-Type": "application/json;charset=UTF-8",
        "psu-corporate-id": "{{.input.psu.corporate_id | default}}",
        "psu-id": "{{.input.psu.id | default}}",
        "psu-ip-address": "{{.input.psu.ip | default}}"
    },
    "info": [
        "GET ACCOUNTS TRACING :  {{.result.headers.X-Yapily-Interaction-Id}}"
    ],
    "method": "GET",
    "name": "Get accounts",
    "retry": "{{.input.retry}}",
    "timeout": "{{.input.timeout}}",
    "tls_client_cert": "{{.certs.client | default}}",
    "tls_client_key": "{{.certs.key | default}}",
    "tls_root_ca": "{{.certs.root_ca | default}}",
    "type": "http",
    "url": "{{.yapily.url}}/accounts",
    "vars": {
        "body": {
            "from": "result.bodyjson.data"
        }
    }
}
`
	content, error := interpolate.Do(payload, vars)
	assert.Nil(t, error)
	assert.NotEmpty(t, content)
}
