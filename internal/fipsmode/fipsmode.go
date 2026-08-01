// Package fipsmode verifies that GitOne is running with a validated version of
// the Go Cryptographic Module in FIPS 140-3 mode.
package fipsmode

import (
	"crypto/fips140"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
)

const (
	Certificate = "CMVP #5247"
	moduleV100  = "v1.0.0-c2097c7c"
	moduleV101  = "v1.0.1"
)

// Must verifies and reports the approved module before an application starts.
// It terminates the process rather than allowing a non-compliant fallback.
func Must() {
	module, err := Require()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("FIPS 140-3 enabled with Go Cryptographic Module %s (%s)", module, Certificate)
}

// Require returns the embedded Go Cryptographic Module version when the
// executable is operating in FIPS 140-3 mode with a version covered by the
// certificate. It fails closed for ordinary, latest, or in-process builds.
func Require() (string, error) {
	module, err := buildModule()
	if err != nil {
		return "", err
	}
	return validate(fips140.Enabled(), module)
}

func validate(enabled bool, module string) (string, error) {
	if !isValidatedModule(module) {
		return "", fmt.Errorf(
			"embedded Go Cryptographic Module %q is not approved for GitOne; build with GOFIPS140=certified",
			module,
		)
	}
	if !enabled {
		return "", errors.New("FIPS 140-3 mode is disabled")
	}
	return module, nil
}

func isValidatedModule(module string) bool {
	return module == moduleV100 || module == moduleV101
}

func buildModule() (string, error) {
	info, ok := debug.ReadBuildInfo()
	return moduleFromBuildInfo(info, ok)
}

func moduleFromBuildInfo(info *debug.BuildInfo, ok bool) (string, error) {
	if !ok {
		return "", errors.New("go build information is unavailable")
	}
	for _, setting := range info.Settings {
		if setting.Key == "GOFIPS140" {
			return setting.Value, nil
		}
	}
	return "", errors.New("GOFIPS140 build setting is missing")
}
