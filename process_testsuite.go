package venom

import (
	"context"
	"github.com/gosimple/slug"
	"time"
)

func (v *Venom) runTestSuite(ctx context.Context, ts *TestSuite) error {

	// Initialize the testsuite variables and compute a first interpolation over them
	ctx = context.WithValue(ctx, ContextKey("testsuite"), ts.Name)
	Info(ctx, "Starting testsuite")
	defer Info(ctx, "Ending testsuite")

	totalSteps := 0
	for _, tc := range ts.TestCases {
		totalSteps += len(tc.testSteps)
	}

	ts.Status = StatusRun
	initialVariables := H{}
	initialVariables.AddAll(ts.Vars)
	for k, value := range *v.InitialVariables {
		initialVariables.Add(k, value)
	}
	// ##### RUN Test Cases Here
	v.runTestCases(ctx, ts, &initialVariables)
	//## Calculate the time here
	ts.End = time.Now()
	ts.Duration = ts.End.Sub(ts.Start).Seconds()
	var isFailed bool
	var nSkip int
	for _, tc := range ts.TestCases {
		if tc.Status == StatusFail {
			isFailed = true
			ts.NbTestcasesFail++
		} else if tc.Status == StatusSkip {
			nSkip++
			ts.NbTestcasesSkip++
		} else if tc.Status == StatusPass {
			ts.NbTestcasesPass++
		}
	}

	if isFailed {
		ts.Status = StatusFail
		v.Tests.NbTestsuitesFail++
	} else if nSkip > 0 && nSkip == len(ts.TestCases) {
		ts.Status = StatusSkip
		v.Tests.NbTestsuitesSkip++
	} else {
		ts.Status = StatusPass
		v.Tests.NbTestsuitesPass++
	}
	//##export report
	err := v.GenerateOutputForTestSuite(ts)
	if err != nil {
		return err
	}
	return nil
}

func (v *Venom) runTestCases(ctx context.Context, ts *TestSuite, variables *H) {
	verboseReport := v.Verbose >= 1

	v.Println(" • %s (%s)", ts.Name, ts.Filepath)
	previousVariables := H{}
	previousVariables.AddAll(*variables)
	for i := range ts.TestCases {
		tc := &ts.TestCases[i]
		tc.IsEvaluated = true
		v.Println(" \t• %s", tc.Name)
		var hasFailure bool
		var hasRanged bool
		var hasSkipped = len(tc.Skipped) > 0
		if !hasSkipped {
			start := time.Now()
			tc.Start = start
			ts.Status = StatusRun
			if verboseReport || hasRanged {
				v.Print("\n")
			}
			// ##### RUN Test Case Here
			computedVariables := v.runTestCase(ctx, tc, &previousVariables)
			previousVariables.AddAllWithPrefix(tc.Name, computedVariables)
			tc.End = time.Now()
			tc.Duration = tc.End.Sub(tc.Start).Seconds()
		}

		skippedSteps := 0
		for _, testStepResult := range tc.TestStepResults {
			if testStepResult.RangedEnable {
				hasRanged = true
			}
			if testStepResult.Status == StatusFail {
				hasFailure = true
			}
			if testStepResult.Status == StatusSkip {
				skippedSteps++
			}
		}

		if hasFailure {
			tc.Status = StatusFail
		} else if skippedSteps == len(tc.TestStepResults) {
			//If all test steps were skipped, consider the test case as skipped
			tc.Status = StatusSkip
		} else if tc.Status != StatusSkip {
			tc.Status = StatusPass
		}

		// Verbose mode already reported tests status, so just print them when non-verbose

		if hasRanged || verboseReport {

			// If the testcase was entirely skipped, then the verbose mode will not have any output
			// Print something to inform that the testcase was indeed processed although skipped
			if len(tc.TestStepResults) == 0 {
				v.Println("\t\t%s", Gray("• (all steps were skipped)"))
				continue
			}
		} else {
			v.Print("status:")
			if hasFailure {
				v.Println(" %s", Red(StatusFail))
			} else if tc.Status == StatusSkip {
				v.Println(" %s", Gray(StatusSkip))
				continue
			} else {
				v.Println(" %s", Green(StatusPass))
			}
		}

		// Verbose mode already reported failures, so just print them when non-verbose
		if !hasRanged && !verboseReport && hasFailure {
			for _, testStepResult := range tc.TestStepResults {
				for _, f := range testStepResult.Errors {
					v.Println("%s", Yellow(f.Value))
				}
			}
		}

		if v.StopOnFailure {
			for _, testStepResult := range tc.TestStepResults {
				if len(testStepResult.Errors) > 0 {
					// break TestSuite
					return
				}
			}
		}

	}

}

// Parse the testscases to find unreplaced and extracted variables
func (v *Venom) parseTestCases(ctx context.Context, ts *TestSuite) ([]string, []string, error) {
	var vars []string
	var extractsVars []string

	for i := range ts.TestCases {
		tc := &ts.TestCases[i]
		tc.originalName = tc.Name
		tc.Name = slug.Make(tc.Name)
		Info(ctx, "Parsing testcase %s ", tc.Name)

		if len(tc.Skipped) == 0 {
			tvars, tExtractedVars, err := v.parseTestCase(ctx, tc)
			if err != nil {
				return nil, nil, err
			}
			for _, k := range tvars {
				var found bool
				for i := 0; i < len(vars); i++ {
					if vars[i] == k {
						found = true
						break
					}
				}
				if !found {
					vars = append(vars, k)
				}
			}
			for _, k := range tExtractedVars {
				var found bool
				for i := 0; i < len(extractsVars); i++ {
					if extractsVars[i] == k {
						found = true
						break
					}
				}
				if !found {
					extractsVars = append(extractsVars, k)
				}
			}
		}
	}

	return vars, extractsVars, nil
}
