package runner

import (
	"os"
	"os/exec"
	"testing"
)

func TestRenderedBuildScriptLogsCommandsWithoutExpandingMarkers(t *testing.T) {
	commands := []string{
		`printf 'value=%s\n' "$EXAMPLE"`,
		"false",
		"echo skipped",
	}
	process := exec.Command("/bin/sh", "-ec", renderBuildScript(commands))
	process.Env = append(os.Environ(), "EXAMPLE=expanded-value")
	output, err := process.CombinedOutput()
	if err == nil {
		t.Fatal("failing build script succeeded")
	}
	want := "$ printf 'value=%s\\n' \"$EXAMPLE\"\n" +
		"value=expanded-value\n" +
		"$ false\n"
	if string(output) != want {
		t.Fatalf("build script output = %q, want %q", output, want)
	}
}

func TestRenderedBuildScriptMarksMultilineCommands(t *testing.T) {
	command := "printf 'first\\n'\nprintf 'second\\n'"
	output, err := exec.Command("/bin/sh", "-ec", renderBuildScript([]string{command})).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	want := "$ printf 'first\\n'\n" +
		"  printf 'second\\n'\n" +
		"first\nsecond\n"
	if string(output) != want {
		t.Fatalf("multiline build script output = %q, want %q", output, want)
	}
}
