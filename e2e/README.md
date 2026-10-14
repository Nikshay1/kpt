# End-to-end testing of kpt

## kpt live e2e tests

We currently have two different solutions for running e2e tests for the kpt
live functionality. We are working on reconciling this into one approach that
we can use consistently.

All e2e tests for live requires that kind is available.

### Testing with go

We have a framework for running e2e tests based on specifying test cases
under the `testdata/live-apply` and `testdata/live-scenarios` folders. The
entry point for the test framework is in the `live_test.go` file, and the
framework itself is in `framework/live`.

In order to run all the tests, there is a make target
```sh
make test-live
```

It is possible to run a single test in `testdata/live-apply` by specifying
the name of the test case:
```sh
make test-live-apply T=crd-and-cr
```

The tests create a kind cluster named `live-e2e-test` once, and leave it
running after the tests. Set `K8S_VERSION` to select the version of the
kind node image.

#### Structure of a test case

Each test case is a folder directly under `testdata/live-apply` or
`testdata/live-scenarios`. In the root of each test case folder there must be
a `config.yaml` file that provides the configuration of the test case (the kpt
commands to run and the expected results). The package is provided in the
`resources` folder. The test case folder is copied to a temporary folder
before the test, and the kpt commands run in the copy of `resources`, so
commands such as `kpt live init` do not change the test files. If there is a
`pre-apply` folder, its manifests are applied with `kubectl apply` first.

The framework creates a namespace with the name of the test case folder and
deletes it after the test.

#### Configuration options

These are the configuration options available in the `config.yaml` file:
 * `parallel`: Defines whether the test case can run in parallel with other test
   cases. A parallel test case must not create, change or delete cluster-scoped
   resources other than its own. Test cases that are not parallel run first, one
   at a time.
 * `noResourceGroup`: Defines that the ResourceGroup CRD is removed from the
   cluster before the test case runs. Only for test cases that are not parallel.
   Otherwise, the framework makes sure the ResourceGroup CRD is installed.
 * `optionalStdOut`: A list of lines that are removed from the output (both
   stdout and stderr) before it is compared, if they appear.
 * `kptArgs`: Defines the arguments that will be used with executing the kpt live command.
 * `exitCode`: Defines the expected exit code after running the kpt live command. Defaults to 0.
 * `stdErr`: Defines the expected output to stdErr after running the command. Defaults to "".
 * `stdOut`: Defines the expected output to stdOut after running the command. Defaults to "".
 * `inventory`: Defines the expected inventory after running the command. It
   is only checked if it is not empty.
 * `steps`: A list of kpt commands that run one after the other in the same
   package. It can not be used together with `kptArgs`, `exitCode`, `stdErr`,
   `stdOut` and `inventory`. The test case stops at the first step that fails.

Before the output is compared, timestamps, UIDs and resource versions are
replaced by placeholders, and the status event lines are removed.

#### Steps

Each step supports these options:
 * `kptArgs`: The arguments for the kpt command. Required.
 * `exitCode`: The expected exit code. Defaults to 0.
 * `stdOut`, `stdErr`: The expected output. If they are not set, the output is
   not compared. Use `""` to require empty output.
 * `stdOutContains`, `stdErrContains`: Strings that the output must contain.
 * `stdOutNotContains`, `stdErrNotContains`: Strings that the output must not
   contain.
 * `files`: Checks on files in the package after the command, each with a
   `path` relative to the package, and `contains` and `notContains` lists of
   strings.

For example:
```yaml
parallel: true
steps:
  - kptArgs: ["live", "init", "--name=my-inventory"]
    stdErrContains:
      - "...success"
    files:
      - path: resourcegroup.yaml
        contains:
          - "name: my-inventory"
  - kptArgs: ["live", "init"]
    exitCode: 1
```

## Testing with bash

This approach uses a bash script that runs through several scenarios for
kpt live in sequence. Run it by running
```sh
./live/end-to-end-test.sh
```