package fipsmode

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestValidatedFIPSBuild(t *testing.T) {
	module, err := Require()
	if err != nil {
		t.Fatal(err)
	}
	if !isValidatedModule(module) {
		t.Fatalf("unexpected validated module %q", module)
	}
}

func TestMustAcceptsValidatedFIPSBuild(_ *testing.T) {
	Must()
}

func TestValidateRejectsNonCompliantModes(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		module  string
		message string
	}{
		{name: "disabled", module: moduleV100, message: "disabled"},
		{name: "ordinary build", enabled: true, message: "not approved"},
		{name: "latest", enabled: true, module: "latest", message: "not approved"},
		{name: "in process", enabled: true, module: "v1.26.0", message: "not approved"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validate(test.enabled, test.module); err == nil ||
				!strings.Contains(err.Error(), test.message) {
				t.Fatalf("validate() error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestValidateAcceptsCertificateModules(t *testing.T) {
	for _, module := range []string{moduleV100, moduleV101} {
		if actual, err := validate(true, module); err != nil || actual != module {
			t.Fatalf("validate(%q) = %q, %v", module, actual, err)
		}
	}
}

func TestModuleFromBuildInfoRejectsMissingInformation(t *testing.T) {
	if _, err := moduleFromBuildInfo(nil, false); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable build information error = %v", err)
	}
	if _, err := moduleFromBuildInfo(&debug.BuildInfo{}, true); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing GOFIPS140 error = %v", err)
	}
}
