package venom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/gosimple/slug"
	"github.com/ovh/cds/sdk/interpolate"
)

type dumpFile struct {
	Variables H           `json:"variables"`
	TestStep  TestStep    `json:"step"`
	Result    interface{} `json:"result"`
}

// RunTestStep executes a venom testcase is a venom context
func (v *Venom) RunTestStep(ctx context.Context, e ExecutorRunner, tc *TestCase, tsResult *TestStepResult, stepNumber int, rangedIndex int, step TestStep, vars *H) (interface{}, H) {
	ctx = context.WithValue(ctx, ContextKey("executor"), e.Name())
	var assertRes AssertionsApplied
	var result interface{}
	newVars := H{}
	for tsResult.Retries = 0; tsResult.Retries <= e.Retry() && !assertRes.OK; tsResult.Retries++ {
		if tsResult.Retries > 1 && !assertRes.OK {
			Debug(ctx, "Sleep %d, it's %d attempt", e.Delay(), tsResult.Retries)
			time.Sleep(time.Duration(e.Delay()) * time.Second)
		}

		var err error
		result, err = v.runTestStepExecutor(ctx, e, tc, tsResult, step, vars)
		if err != nil {
			// we save the failure only if it's the last attempt
			if tsResult.Retries == e.Retry() {
				failure := newFailure(ctx, *tc, stepNumber, rangedIndex, "", err)
				tsResult.appendFailure(*failure)
			}
			continue
		}

		Debug(ctx, "result of runTestStepExecutor: %+v", result)

		mapResultString, _ := DumpString(result)
		for k, value := range mapResultString {
			tsResult.ComputedVars.Add(k, value)
			newVars.Add(k, value)
		}
		tsResult.ComputedVars.AddAll(AllVarsFromCtx(ctx))
		if v.Verbose >= 2 {
			fdump := dumpFile{
				Result:    result,
				TestStep:  step,
				Variables: tsResult.ComputedVars,
			}
			output, err := json.MarshalIndent(fdump, "", " ")
			if err != nil {
				Error(ctx, "unable to marshal result: %v", err)
			}

			oDir := v.OutputDir
			if oDir == "" {
				oDir = "."
			}
			filename := path.Join(oDir, fmt.Sprintf("%s.%s.%s.dump.json", slug.Make(StringVarFromCtx(ctx, "venom.testsuite.shortName")), time.Now().UTC().Format("15.04.05"), slug.Make(tc.Name)))

			if err := os.WriteFile(filename, []byte(output), 0644); err != nil {
				Error(ctx, "Error while creating file %s: %v", filename, err)
				return result, tsResult.ComputedVars
			}
			tsResult.ComputedVerbose = append(tsResult.ComputedVerbose, fmt.Sprintf("writing %s", filename))
			tc.computedVerbose = append(tc.computedVerbose, fmt.Sprintf("writing %s", filename))
		}

		allvars, _ := DumpStringPreserveCase(tsResult.ComputedVars)

		for ninfo, value := range e.Info() {
			info, err := interpolate.Do(value, allvars)
			if err != nil {
				Error(ctx, "unable to parse %q: %v", value, err)
				continue
			}
			if info == "" {
				continue
			}
			filename := StringVarFromCtx(ctx, "venom.testsuite.filename")
			lineNumber := findLineNumber(filename, tc.originalName, stepNumber, value, ninfo+1)
			if lineNumber > 0 {
				info += fmt.Sprintf(" (%s:%d)", filename, lineNumber)
			} else if tc.IsExecutor {
				filename = StringVarFromCtx(ctx, "venom.executor.filename")
				originalName := StringVarFromCtx(ctx, "venom.executor.name")
				lineNumber = findLineNumber(filename, originalName, stepNumber, value, ninfo+1)
				if lineNumber > 0 {
					info += fmt.Sprintf(" (%s:%d)", filename, lineNumber)
				}
			}
			Info(ctx, info)
			v.Println("\t  %s%s %s", "\t  ", Cyan("[info]"), Cyan(info))
		}

		if result == nil {
			Debug(ctx, "empty testcase, applying assertions on variables: %v", AllVarsFromCtx(ctx))
			assertRes = applyAssertions(ctx, AllVarsFromCtx(ctx), *tc, stepNumber, rangedIndex, step, nil)
		} else {
			if h, ok := e.(executorWithDefaultAssertions); ok {
				assertRes = applyAssertions(ctx, result, *tc, stepNumber, rangedIndex, step, h.GetDefaultAssertions())
			} else {
				assertRes = applyAssertions(ctx, result, *tc, stepNumber, rangedIndex, step, nil)
			}
		}

		tsResult.AssertionsApplied = assertRes

		if assertRes.OK {
			break
		}
		if len(e.RetryIf()) > 0 {
			failures, err := testConditionalStatement(ctx, tc, e.RetryIf(), &tsResult.ComputedVars, "")
			if err != nil {
				tsResult.appendError(fmt.Errorf("Error while evaluating retry condition: %v", err))
				break
			}
			if len(failures) > 0 {
				failure := newFailure(ctx, *tc, stepNumber, rangedIndex, "", fmt.Errorf("retry conditions not fulfilled, skipping %d remaining retries", e.Retry()-tsResult.Retries))
				tsResult.Errors = append(tsResult.Errors, *failure)
				break
			}
		}
	}

	if tsResult.Retries > 1 && len(assertRes.errors) > 0 {
		tsResult.appendFailure(Failure{Value: fmt.Sprintf("It's a failure after %d attempts", tsResult.Retries)})
	}

	if len(assertRes.errors) > 0 {
		tsResult.appendFailure(assertRes.errors...)
	}

	tsResult.Systemerr += assertRes.systemerr + "\n"
	tsResult.Systemout += assertRes.systemout + "\n"

	return result, newVars
}

func (v *Venom) runTestStepExecutor(ctx context.Context, e ExecutorRunner, tc *TestCase, testStepResult *TestStepResult, step TestStep, vars *H) (interface{}, error) {
	ctx = context.WithValue(ctx, ContextKey("executor"), e.Name())
	v.Print("\n\t\t • %v", e.Name())
	if e.Timeout() == 0 {
		if e.Type() == "user" {
			return v.RunUserExecutor(ctx, e, tc, testStepResult, step, vars)
		}
		return e.Run(ctx, step)
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, time.Duration(e.Timeout())*time.Second)
	defer cancel()

	ch := make(chan interface{})
	cherr := make(chan error)
	go func(e ExecutorRunner, step TestStep) {
		var err error
		var result interface{}
		if e.Type() == "user" {
			result, err = v.RunUserExecutor(ctx, e, tc, testStepResult, step, vars)
		} else {
			result, err = e.Run(ctx, step)
		}
		if err != nil {
			cherr <- err
		} else {
			ch <- result
		}
	}(e, step)

	select {
	case err := <-cherr:
		v.Println(" %s", Red(StatusFail))
		return nil, err
	case result := <-ch:
		v.Println(" %s", Green(StatusPass))
		return result, nil
	case <-ctxTimeout.Done():
		v.Println(" %s", Yellow("OUT OF TIME"))
		return nil, fmt.Errorf("Timeout after %d second(s)", e.Timeout())
	}
}
