package venom

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ovh/cds/sdk/interpolate"
	"github.com/rockbears/yaml"
)

var varRegEx = regexp.MustCompile("{{.*}}")

// Parse the testcase to find unreplaced and extracted variables
func (v *Venom) parseTestCase(ctx context.Context, tc *TestCase) ([]string, []string, error) {
	vars := []string{}
	extractedVars := []string{}

	initialVariables := *v.InitialVariables
	Info(ctx, "%v", initialVariables)
	for _, rawStep := range tc.RawTestSteps {
		content, err := interpolate.Do(string(rawStep), initialVariables)
		if err != nil {
			return nil, nil, err
		}

		var step TestStep
		if err := yaml.Unmarshal([]byte(content), &step); err != nil {
			return nil, nil, errors.Wrapf(err, "unable to unmarshal teststep")
		}

		contextVariables := H{}
		for k, value := range initialVariables {
			contextVariables.Add(k, value)
		}

		_, exec, err := v.GetExecutorRunner(context.Background(), &step, &contextVariables)
		if err != nil {
			return nil, nil, err
		}

		defaultResult := exec.ZeroValueResult()
		if defaultResult != nil {
			dumpE, err := DumpString(defaultResult)
			if err != nil {
				return nil, nil, err
			}
			for k := range dumpE {
				var found bool
				for i := 0; i < len(vars); i++ {
					if vars[i] == k {
						found = true
						break
					}
				}
				if !found {
					extractedVars = append(extractedVars, k)
				}
				extractedVars = append(extractedVars, tc.Name+"."+k)
				if strings.HasSuffix(k, "__type__") && dumpE[k] == "Map" {
					// go-dump doesnt dump the map name, here is a workaround
					k = strings.TrimSuffix(k, "__type__")
					extractedVars = append(extractedVars, tc.Name+"."+k)
				}
			}
		}

		dumpE, err := DumpStringPreserveCase(step)
		if err != nil {
			return nil, nil, err
		}

		for k, v := range dumpE {
			if strings.HasPrefix(k, "vars.") {
				s := tc.Name + "." + strings.Split(k[5:], ".")[0]
				extractedVars = append(extractedVars, s)
				continue
			}
			if strings.HasPrefix(k, "range.") {
				continue
			}
			if strings.HasPrefix(k, "extracts.") {
				s := tc.Name + "." + strings.Split(k[9:], ".")[0]
				extractedVars = append(extractedVars, s)
				continue
			}
			if strings.HasPrefix(k, "info") {
				continue
			}
			if varRegEx.MatchString(v) {
				var found bool
				for i := 0; i < len(vars); i++ {
					if vars[i] == k {
						found = true
						break
					}
				}

				submatches := varRegEx.FindStringSubmatch(v)
				for submatcheIndex, s := range submatches {
					if submatcheIndex == 0 {
						continue
					}
					for i := 0; i < len(extractedVars); i++ {
						prefix := "{{." + extractedVars[i]
						if strings.HasPrefix(s, prefix) {
							found = true
							break
						}
					}
					if !found {
						vars = append(vars, s)

						s = strings.ReplaceAll(s, "{{ .", "")
						s = strings.ReplaceAll(s, "{{.", "")
						s = strings.ReplaceAll(s, "}}", "")
						vars = append(vars, s)
					}
				}
			}
		}
	}
	return vars, extractedVars, nil
}

func (v *Venom) runTestCase(ctx context.Context, tc *TestCase, testSuiteVariables *H) H {
	ctx = context.WithValue(ctx, ContextKey("testcase"), tc.Name)
	Info(ctx, "Starting testcase")
	// ##### RUN Test Steps Here
	computedVars := v.runTestSteps(ctx, tc, testSuiteVariables, nil)
	cleanVars := H{}
	local := *testSuiteVariables
	for k, _ := range computedVars {
		_, exists := local[k]
		if !exists {
			cleanVars.Add(k, computedVars[k])
		}
	}
	Info(ctx, "Ending testcase")
	return cleanVars
}

func (v *Venom) runTestSteps(ctx context.Context, tc *TestCase, testSuiteVariables *H, tsIn *TestStepResult) H {
	if len(tc.Skip) > 0 {

		failures, err := testConditionalStatement(ctx, tc, tc.Skip, testSuiteVariables, "skipping testcase %q: %v")
		if err != nil {
			Error(ctx, "unable to evaluate \"skip\" assertions: %v", err)
			testStepResult := TestStepResult{}
			testStepResult.appendError(err)
			tc.TestStepResults = append(tc.TestStepResults, testStepResult)
			return nil
		}
		if len(failures) > 0 {
			Info(ctx, fmt.Sprintf("Skipping test case as there are %v failures", len(failures)))
			tc.Status = StatusSkip
			for _, s := range failures {
				tc.Skipped = append(tc.Skipped, Skipped{Value: s})
				Warn(ctx, s)
			}
			return nil
		}
	}

	var knowExecutors = map[string]struct{}{}
	previousStepVars := H{}
	previousStepVars.AddAll(*testSuiteVariables)
	onlyNewVars := H{}

	fromUserExecutor := tsIn != nil

	for stepNumber, rawStep := range tc.RawTestSteps {
		stepVars := H{}
		stepVars.AddAll(previousStepVars)
		stepVars.Add("venom.testcase", tc.Name)
		stepVars.Add("venom.teststep.number", stepNumber)

		ranged, err := parseRanged(ctx, rawStep, &stepVars)
		if err != nil {
			Error(ctx, "unable to parse \"range\" attribute: %v", err)
			tsIn.appendError(err)
			return nil
		}

		for rangedIndex, rangedData := range ranged.Items {
			tc.TestStepResults = append(tc.TestStepResults, TestStepResult{})
			tsResult := &tc.TestStepResults[len(tc.TestStepResults)-1]

			if ranged.Enabled {
				Debug(ctx, "processing range index: %d", rangedIndex)
				stepVars.Add("index", rangedIndex)
				stepVars.Add("key", rangedData.Key)
				stepVars.Add("value", rangedData.Value)
			}

			vars, err := DumpStringPreserveCase(stepVars)
			if err != nil {
				Error(ctx, "unable to dump testcase vars: %v", err)
				tsResult.appendError(err)
				return nil
			}

			for k, value := range vars {
				content, err := interpolate.Do(value, vars)
				if err != nil {
					tsResult.appendError(err)
					Error(ctx, "unable to interpolate variable %q: %v", k, err)
					return nil
				}
				vars[k] = content
			}

			// the value of each var can contains a double-quote -> "
			// if the value is not escaped, it will be used as is, and the json sent to unmarshall will be incorrect.
			// This also avoids injections into the json structure of a step
			for i := range vars {
				if strings.Contains(vars[i], `"`) {
					x := strconv.Quote(vars[i])
					x = strings.TrimPrefix(x, `"`)
					x = strings.TrimSuffix(x, `"`)
					vars[i] = x
				}
			}

			var content string
			for i := 0; i < 10; i++ {
				content, err = interpolate.Do(string(rawStep), vars)
				if err != nil {
					tsResult.appendError(err)
					Error(ctx, "unable to interpolate step: %v", err)
					return nil
				}
				if !strings.Contains(content, "{{.") {
					break
				}
			}
			if strings.Contains(content, "{{.") {
				Error(ctx, "We could not replace all variables")
			}

			if ranged.Enabled {
				Info(ctx, "Step #%d-%d content is: %s", stepNumber, rangedIndex, content)
			} else {
				Info(ctx, "Step #%d content is: %s", stepNumber, content)
			}

			data, err := yaml.Marshal(rawStep)
			if err != nil {
				tsResult.appendError(err)
				Error(ctx, "unable to marshal raw: %v", err)
			}
			tsResult.Raw = data

			var step TestStep
			if err := yaml.Unmarshal([]byte(content), &step); err != nil {
				tsResult.appendError(err)
				Error(ctx, "unable to parse step #%d: %v", stepNumber, err)
				return nil
			}

			data2, err := yaml.JSONToYAML([]byte(content))
			if err != nil {
				tsResult.appendError(err)
				Error(ctx, "unable to marshal step #%d to json: %v", stepNumber, err)
			}
			tsResult.Interpolated = data2

			tsResult.Number = stepNumber
			tsResult.RangedIndex = rangedIndex
			tsResult.RangedEnable = ranged.Enabled
			tsResult.InputVars = vars

			tc.testSteps = append(tc.testSteps, step)
			var runner ExecutorRunner
			Info(ctx, "variables before execution %v", stepVars)
			ctx, runner, err = v.GetExecutorRunner(ctx, &step, &stepVars)
			if err != nil {
				tsResult.appendError(err)
				Error(ctx, "unable to get executor: %v", err)
				break
			}

			if runner != nil {
				_, known := knowExecutors[runner.Name()]
				if !known {
					ctx, err = runner.Setup(ctx, stepVars)
					if err != nil {
						tsResult.appendError(err)
						Error(ctx, "unable to setup executor: %v", err)
						break
					}
					knowExecutors[runner.Name()] = struct{}{}
					if err := runner.TearDown(ctx); err != nil {
						tsResult.appendError(err)
						Error(ctx, "unable to teardown executor: %v", err)
						break
					}
				}
			}

			printStepName := v.Verbose >= 1 && !fromUserExecutor
			v.setTestStepName(tsResult, runner, step, &ranged, &rangedData, rangedIndex, printStepName)

			// ##### RUN Test Step Here
			Info(ctx, fmt.Sprintf("Checking skip for test step %v", printStepName))
			skip, err := parseSkip(ctx, tc, &stepVars, tsResult, rawStep, stepNumber)
			if err != nil {
				tsResult.appendError(err)
				tsResult.Status = StatusFail
			} else if skip {
				tsResult.Status = StatusSkip
			} else {
				tsResult.Start = time.Now()
				tsResult.Status = StatusRun
				_, vars := v.RunTestStep(ctx, runner, tc, tsResult, stepNumber, rangedIndex, step, &previousStepVars)
				if len(tsResult.Errors) > 0 || !tsResult.AssertionsApplied.OK {
					tsResult.Status = StatusFail
				} else {
					tsResult.Status = StatusPass
				}
				tsResult.ComputedVars.AddAll(vars)

				tsResult.End = time.Now()
				tsResult.Duration = tsResult.End.Sub(tsResult.Start).Seconds()

				//mapResult := GetExecutorResult(result)
				previousStepVars.AddAll(vars)

				tc.testSteps = append(tc.testSteps, step)
			}

			var isRequired bool

			if tsResult.Status == StatusFail {
				Error(ctx, "Errors: ")
				for _, e := range tsResult.Errors {
					Error(ctx, "%v", e)
					isRequired = isRequired || e.AssertionRequired
				}

				if isRequired {
					failure := newFailure(ctx, *tc, stepNumber, rangedIndex, "", fmt.Errorf("At least one required assertion failed, skipping remaining steps"))
					tsResult.appendFailure(*failure)
					v.printTestStepResult(tc, tsResult, tsIn, ranged, stepNumber, true)
					return nil
				}
				v.printTestStepResult(tc, tsResult, tsIn, ranged, stepNumber, false)
				continue
			}
			v.printTestStepResult(tc, tsResult, tsIn, ranged, stepNumber, false)

			//tsResult.ComputedVars = tc.computedVars.Clone()

			assign, _, err := processVariableAssignments(ctx, tc.Name, &tsResult.ComputedVars, rawStep)
			if err != nil {
				tsResult.appendError(err)
				Error(ctx, "unable to process variable assignments: %v", err)
				break
			}
			if assign != nil {
				tsResult.ComputedVars.AddAll(assign)
				onlyNewVars.AddAll(assign)
				previousStepVars.AddAll(assign)
			}
		}
	}
	return onlyNewVars
}

// Set test step name (defaults to executor name, excepted if it got a "name" attribute. in range, also print key)
func (v *Venom) setTestStepName(ts *TestStepResult, e ExecutorRunner, step TestStep, ranged *Range, rangedData *RangeData, rangedIndex int, print bool) {
	name := e.Name()
	if value, ok := step["name"]; ok {
		switch value := value.(type) {
		case string:
			name = value
		}
	}
	if ranged.Enabled {
		if rangedIndex == 0 {
			v.Println("\n")
		}
		name = fmt.Sprintf("%s (range=%s)", name, rangedData.Key)
	}
	ts.Name = name

	if print || ranged.Enabled {
		v.Println(" \t\t• %s", ts.Name)
	}
}

// Print a single step result (if verbosity is enabled)
func (v *Venom) printTestStepResult(tc *TestCase, ts *TestStepResult, tsIn *TestStepResult, ranged Range, stepNumber int, mustAssertionFailed bool) {
	fromUserExecutor := tsIn != nil
	if fromUserExecutor {
		tsIn.appendFailure(ts.Errors...)
	}
	if ranged.Enabled || v.Verbose >= 1 {
		if !fromUserExecutor { //Else print step status
			if len(ts.Errors) > 0 {
				v.Println(" %s", Red(StatusFail))
				for _, f := range ts.Errors {
					v.Println(" \t\t  %s", Yellow(f.Value))
				}
				if mustAssertionFailed {
					skipped := len(tc.RawTestSteps) - stepNumber - 1
					if skipped == 1 {
						v.Println(" \t\t  %s", Gray(fmt.Sprintf("%d other step was skipped", skipped)))
					} else {
						v.Println(" \t\t  %s", Gray(fmt.Sprintf("%d other steps were skipped", skipped)))
					}
				}
			} else if ts.Status == StatusSkip {
				v.Println(" %s", Gray(StatusSkip))
			} else {
				if ts.Retries == 0 {
					v.Println(" %s", Green(StatusPass))
				} else {
					v.Println(" %s (after %d attempts)", Green(StatusPass), ts.Retries)
				}
			}
			for _, i := range ts.ComputedInfo {
				v.Println("\t  %s%s %s", "\t  ", Cyan("[info]"), Cyan(i))
			}
			for _, i := range ts.ComputedVerbose {
				v.PrintlnIndentedTrace(i, "\t  ")
			}

		}
	}
}

// Parse and format skip conditional
func parseSkip(ctx context.Context, tc *TestCase, vars *H, ts *TestStepResult, rawStep []byte, stepNumber int) (bool, error) {
	// Load "skip" attribute from step
	var assertions struct {
		Skip []string `yaml:"skip"`
	}
	if err := yaml.Unmarshal(rawStep, &assertions); err != nil {
		return false, fmt.Errorf("unable to parse \"skip\" assertions: %v", err)
	}

	// Evaluate skip assertions
	if len(assertions.Skip) > 0 {
		failures, err := testConditionalStatement(ctx, tc, assertions.Skip, vars, fmt.Sprintf("skipping testcase %%q step #%d: %%v", stepNumber))
		if err != nil {
			Error(ctx, "unable to evaluate \"skip\" assertions: %v", err)
			return false, err
		}

		if len(failures) > 0 {
			Info(ctx, fmt.Sprintf("Skip as there are %v failures", len(failures)))
			for _, s := range failures {
				ts.Skipped = append(ts.Skipped, Skipped{Value: s})
				Warn(ctx, s)
			}
			return true, nil
		}
	}
	return false, nil
}

// Parse and format range data to allow iterations over user data
func parseRanged(ctx context.Context, rawStep []byte, stepVars *H) (Range, error) {

	//Load "range" attribute and perform actions depending on its typing
	var ranged Range
	if err := json.Unmarshal(rawStep, &ranged); err != nil {
		return ranged, fmt.Errorf("unable to parse range expression: %v", err)
	}

	switch ranged.RawContent.(type) {

	//Nil means this is not a ranged data, append an empty item to force at least one iteration and exit
	case nil:
		ranged.Items = append(ranged.Items, RangeData{})
		return ranged, nil

	//String needs to be parsed and possibly templated
	case string:
		Debug(ctx, "attempting to parse range expression")
		rawString := ranged.RawContent.(string)
		if len(rawString) == 0 {
			return ranged, fmt.Errorf("range expression has been specified without any data")
		}

		// Try parsing already templated data
		err := json.Unmarshal([]byte("{\"range\":"+rawString+"}"), &ranged)
		// ... or fallback
		if err != nil {
			//Try templating and escaping data
			Debug(ctx, "attempting to template range expression and parse it again")
			vars, err := DumpStringPreserveCase(stepVars)
			if err != nil {
				Warn(ctx, "failed to parse range expression when loading step variables: %v", err)
				break
			}
			for i := range vars {
				vars[i] = strings.ReplaceAll(vars[i], "\"", "\\\"")
			}
			content, err := interpolate.Do(string(rawStep), vars)
			if err != nil {
				Warn(ctx, "failed to parse range expression when templating variables: %v", err)
				break
			}

			//Try parsing data
			err = json.Unmarshal([]byte(content), &ranged)
			if err != nil {
				Warn(ctx, "failed to parse range expression when parsing data into raw string: %v", err)
				break
			}
			switch ranged.RawContent.(type) {
			case string:
				rawString = ranged.RawContent.(string)
				err := json.Unmarshal([]byte("{\"range\":"+rawString+"}"), &ranged)
				if err != nil {
					Warn(ctx, "failed to parse range expression when parsing raw string into data: %v", err)
					return ranged, fmt.Errorf("unable to parse range expression: unable to transform string data into a supported range expression type")
				}
			}
		}
	}

	//Format data
	switch t := ranged.RawContent.(type) {

	//Array-like data
	case []interface{}:
		Debug(ctx, "\"range\" data is array-like")
		for index, value := range ranged.RawContent.([]interface{}) {
			key := strconv.Itoa(index)
			ranged.Items = append(ranged.Items, RangeData{key, value})
		}

	//Number data
	case float64:
		Debug(ctx, "\"range\" data is number-like")
		upperBound := int(ranged.RawContent.(float64))
		for i := 0; i < upperBound; i++ {
			key := strconv.Itoa(i)
			ranged.Items = append(ranged.Items, RangeData{key, i})
		}

	//Map-like data
	case map[string]interface{}:
		Debug(ctx, "\"range\" data is map-like")
		for key, value := range ranged.RawContent.(map[string]interface{}) {
			ranged.Items = append(ranged.Items, RangeData{key, value})
		}

	//Unsupported data format
	default:
		return ranged, fmt.Errorf("\"range\" was provided an unsupported type %T", t)
	}

	ranged.Enabled = true
	ranged.RawContent = nil
	return ranged, nil
}

func processVariableAssignments(ctx context.Context, tcName string, tcVars *H, rawStep json.RawMessage) (H, bool, error) {
	var stepAssignment AssignStep
	var result = make(H)
	if err := yaml.Unmarshal(rawStep, &stepAssignment); err != nil {
		Error(ctx, "unable to parse assignments (%s): %v", string(rawStep), err)
		return nil, false, err
	}

	if len(stepAssignment.Assignments) == 0 {
		return nil, false, nil
	}

	localVars := *tcVars
	var tcVarsKeys []string
	for k := range localVars {
		tcVarsKeys = append(tcVarsKeys, k)
	}

	for varname, assignment := range stepAssignment.Assignments {
		Debug(ctx, "Processing %s assignment", varname)
		varValue, has := localVars[assignment.From]
		if !has {
			varValue, has = localVars[tcName+"."+assignment.From]
			if !has {
				if assignment.Default == nil {
					err := fmt.Errorf("%s reference not found in %s", assignment.From, strings.Join(tcVarsKeys, "\n"))
					Info(ctx, "%v", err)
					return nil, true, err
				}
				varValue = assignment.Default
			}
		}
		if assignment.Regex == "" {
			Info(ctx, "Assign '%s' value '%s'", varname, varValue)
			result.Add(varname, varValue)
		} else {
			regex, err := regexp.Compile(assignment.Regex)
			if err != nil {
				Warn(ctx, "unable to compile regexp %q", assignment.Regex)
				return nil, true, err
			}
			varValueS, ok := varValue.(string)
			if !ok {
				Warn(ctx, "%q is not a string value", varname)
				result.Add(varname, "")
				continue
			}
			submatches := regex.FindStringSubmatch(varValueS)
			if len(submatches) == 0 {
				Warn(ctx, "%s: %q doesn't match anything in %q", varname, regex, varValue)
				result.Add(varname, "")
				continue
			}
			Info(ctx, "Assign %q from regexp %q, values %q", varname, regex, submatches)
			result.Add(varname, submatches[len(submatches)-1])
		}
	}
	return result, true, nil
}
