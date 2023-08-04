package venom

import (
	"bufio"
	"context"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pkg/errors"
	"github.com/rockbears/yaml"
)

func getUserExecutorInputYML(ctx context.Context, btesIn []byte) (H, error) {
	btes := readPartialYML(btesIn, "input")

	var result = map[string]interface{}{}
	var tmpResult = map[string]interface{}{}

	if len(btes) > 0 {
		if err := yaml.Unmarshal([]byte(btes), &tmpResult); err != nil {
			return nil, err
		}
	}
	tmp, ok := tmpResult["foo"]
	if ok {
		if reflect.ValueOf(tmp).Kind() == reflect.Map {
			result = tmp.(map[string]interface{})
		}
	}

	return result, nil
}

func getVarFromPartialYML(ctx context.Context, btesIn []byte) (H, error) {
	btes := readPartialYML(btesIn, "vars")
	type partialVars struct {
		Vars H `yaml:"vars" json:"vars"`
	}
	var partial partialVars
	if len(btes) > 0 {
		if err := yaml.Unmarshal([]byte(btes), &partial); err != nil {
			Error(context.Background(), "file content: %s", string(btes))
			return nil, errors.Wrapf(err, "error while unmarshal - see venom.log")
		}
	}
	return partial.Vars, nil
}

func getExecutorName(btes []byte) (string, error) {
	content := readPartialYML(btes, "executor")
	type partialType struct {
		Executor string `yaml:"executor" json:"executor"`
	}
	partial := &partialType{}
	if len(content) > 0 {
		if err := yaml.Unmarshal([]byte(content), &partial); err != nil {
			Error(context.Background(), "file content: %s", string(btes))
			return "", errors.Wrapf(err, "error while unmarshal - see venom.log")
		}
	}
	return partial.Executor, nil
}

// readPartialYML extract a yml part from a given string
func readPartialYML(btes []byte, attribute string) string {
	var result []string
	scanner := bufio.NewScanner(strings.NewReader(string(btes)))

	var record bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, attribute+":") {
			record = true
		} else if len(line) > 0 {
			c, _ := utf8.DecodeRuneInString(line[0:1])
			if !unicode.IsSpace(c) && !strings.HasPrefix(line, "-") {
				record = false
			}
		}
		if record {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}
